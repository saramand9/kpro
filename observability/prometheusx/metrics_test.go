package prometheusx

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/saramand9/kpro/core"
)

func TestMetricsRecordBoundedInvocationLabels(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := New(Config{Registerer: registry, Gatherer: registry})
	if err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("failed")
	handler := metrics.Middleware()(func(core.Invocation) error { return wantErr })
	invocation := core.NewInvocation(core.InvocationOptions{
		Context:   context.Background(),
		Transport: core.TransportHTTP,
		Operation: "GET /health",
	})
	if err := handler(invocation); !errors.Is(err, wantErr) {
		t.Fatalf("handler error = %v", err)
	}

	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	body := recorder.Body.String()
	if !strings.Contains(body, `kpro_invocations_total{operation="GET /health",result="error",transport="http"} 1`) {
		t.Fatalf("metrics output missing invocation counter:\n%s", body)
	}
	if !strings.Contains(body, `kpro_invocations_inflight{operation="GET /health",transport="http"} 0`) {
		t.Fatalf("metrics output missing zero in-flight gauge:\n%s", body)
	}
}
