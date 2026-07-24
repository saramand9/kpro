package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/saramand9/kpro/database/common/lifecycle"
	"github.com/saramand9/kpro/internal/app/persistence"
	"github.com/saramand9/kpro/middleware"
	http3x "github.com/saramand9/kpro/transport/http3"
	"github.com/saramand9/kpro/transport/httpx"
)

type testResourceHealth struct {
	managed bool
	health  func(context.Context) []lifecycle.Health
}

func (r *testResourceHealth) Managed() bool {
	return r != nil && r.managed
}

func (r *testResourceHealth) Health(ctx context.Context) []lifecycle.Health {
	if r == nil || r.health == nil {
		return nil
	}
	return r.health(ctx)
}

func TestReferenceProbeRoutesMatchAcrossHTTPVersions(t *testing.T) {
	const internalError = "postgres://user:secret@database:5432/app connection refused"
	unhealthy := &testResourceHealth{
		managed: true,
		health: func(context.Context) []lifecycle.Health {
			return []lifecycle.Health{{
				Name:   "app",
				Kind:   lifecycle.KindSQL,
				Status: lifecycle.StatusDown,
				Error:  internalError,
			}}
		},
	}

	for _, endpoint := range referenceProbeEndpoints(unhealthy) {
		t.Run(endpoint.name, func(t *testing.T) {
			assertProbe(t, endpoint.handler, "/livez", http.StatusOK, "alive")
			assertProbe(t, endpoint.handler, "/readyz", http.StatusServiceUnavailable, "not_ready")
			response := assertProbe(t, endpoint.handler, "/health", http.StatusServiceUnavailable, "not_ready")
			if strings.Contains(response, internalError) || strings.Contains(response, "secret") {
				t.Fatalf("readiness response leaked resource error: %q", response)
			}
		})
	}
}

func TestReferenceLivenessDoesNotCheckResources(t *testing.T) {
	healthCalls := 0
	resources := &testResourceHealth{
		managed: true,
		health: func(context.Context) []lifecycle.Health {
			healthCalls++
			return nil
		},
	}
	for _, endpoint := range referenceProbeEndpoints(resources) {
		t.Run(endpoint.name, func(t *testing.T) {
			assertProbe(t, endpoint.handler, "/livez", http.StatusOK, "alive")
		})
	}
	if healthCalls != 0 {
		t.Fatalf("liveness checked database health %d times", healthCalls)
	}
}

func TestReferenceReadinessSucceedsWithoutDatabase(t *testing.T) {
	resources, err := persistence.Open(context.Background(), persistence.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if resources.Managed() {
		t.Fatal("disabled persistence unexpectedly owns a resource")
	}

	for _, endpoint := range referenceProbeEndpoints(resources) {
		t.Run(endpoint.name, func(t *testing.T) {
			assertProbe(t, endpoint.handler, "/readyz", http.StatusOK, "ready")
			assertProbe(t, endpoint.handler, "/health", http.StatusOK, "ready")
		})
	}
}

func TestReferenceReadinessUsesDatabasePing(t *testing.T) {
	resources, err := persistence.Open(context.Background(), persistence.Config{
		SQL: persistence.SQLConfig{Driver: "sqlite", DSN: ":memory:"},
	})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := resources.SQL.SQLDB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}

	for _, endpoint := range referenceProbeEndpoints(resources) {
		t.Run(endpoint.name, func(t *testing.T) {
			assertProbe(t, endpoint.handler, "/livez", http.StatusOK, "alive")
			assertProbe(t, endpoint.handler, "/readyz", http.StatusServiceUnavailable, "not_ready")
		})
	}
}

func TestResourcesReadyBoundsHealthCheck(t *testing.T) {
	const internalError = "dial tcp database.internal:5432: connection timed out"
	resources := &testResourceHealth{
		managed: true,
		health: func(ctx context.Context) []lifecycle.Health {
			<-ctx.Done()
			return []lifecycle.Health{{Status: lifecycle.StatusDown, Error: internalError}}
		},
	}
	started := time.Now()
	if resourcesReady(context.Background(), resources, 20*time.Millisecond) {
		t.Fatal("timed-out resource was reported ready")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("bounded readiness check took %s", elapsed)
	}

	endpoint := httpx.New(httpx.Config{})
	endpoint.Get("/readyz", readinessHandlerWithTimeout(resources, 20*time.Millisecond))
	response := assertProbe(t, endpoint, "/readyz", http.StatusServiceUnavailable, "not_ready")
	if strings.Contains(response, internalError) || strings.Contains(response, "database.internal") {
		t.Fatalf("timed-out readiness response leaked resource error: %q", response)
	}
}

type namedHTTPHandler struct {
	name    string
	handler http.Handler
}

func referenceProbeEndpoints(resources appResourceHealth) []namedHTTPHandler {
	metrics := middleware.NewMetrics()
	httpEndpoint := httpx.New(httpx.Config{})
	registerHTTPRoutes(httpEndpoint, metrics, appServices{}, resources)
	http3Endpoint := http3x.New(http3x.Config{})
	registerHTTP3Routes(http3Endpoint, metrics, resources)
	return []namedHTTPHandler{
		{name: "http", handler: httpEndpoint},
		{name: "http3", handler: http3Endpoint},
	}
}

func assertProbe(t *testing.T, handler http.Handler, path string, wantStatus int, wantProbeStatus string) string {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != wantStatus {
		t.Fatalf("GET %s status = %d, want %d; body=%q", path, response.Code, wantStatus, response.Body.String())
	}
	var payload probeResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("GET %s decode response: %v; body=%q", path, err, response.Body.String())
	}
	if payload.Status != wantProbeStatus {
		t.Fatalf("GET %s probe status = %q, want %q", path, payload.Status, wantProbeStatus)
	}
	return response.Body.String()
}
