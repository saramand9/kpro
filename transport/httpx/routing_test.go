package httpx

import (
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/saramand9/kpro/core"
)

func TestPatternRouteExposesPathParameters(t *testing.T) {
	endpoint := New(Config{})
	endpoint.Get("/users/{id}", func(ctx *Context) error {
		if ctx.Request() == nil || ctx.ResponseWriter() == nil {
			t.Fatal("native HTTP escape hatches are nil")
		}
		return ctx.Text(http.StatusOK, ctx.Param("id"))
	})

	response := executeTestRequest(endpoint, http.MethodGet, "/users/42", "")
	if response.Code != http.StatusOK || response.Body.String() != "42" {
		t.Fatalf("response = %d/%q, want 200/42", response.Code, response.Body.String())
	}
}

func TestRouteGroupAndRouteMiddlewareOrder(t *testing.T) {
	endpoint := New(Config{})
	var order []string
	middleware := func(label string) core.Middleware {
		return func(next core.InvocationHandler) core.InvocationHandler {
			return func(invocation core.Invocation) error {
				order = append(order, label)
				return next(invocation)
			}
		}
	}
	endpoint.Use(middleware("endpoint"))
	api := endpoint.Group("/api", middleware("group"))
	api.Get("/users/{id}", func(ctx *Context) error {
		order = append(order, "handler:"+ctx.Param("id"))
		return ctx.Text(http.StatusOK, "ok")
	}, middleware("route"))

	response := executeTestRequest(endpoint, http.MethodGet, "/api/users/7", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.Code)
	}
	want := []string{"endpoint", "group", "route", "handler:7"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("middleware order = %v, want %v", order, want)
	}
}

func TestNestedRouteGroup(t *testing.T) {
	endpoint := New(Config{})
	endpoint.Group("/api").Group("/v1").Get("health", func(ctx *Context) error {
		return ctx.Text(http.StatusOK, "ok")
	})

	response := executeTestRequest(endpoint, http.MethodGet, "/api/v1/health", "")
	if response.Code != http.StatusOK || response.Body.String() != "ok" {
		t.Fatalf("response = %d/%q, want 200/ok", response.Code, response.Body.String())
	}
}

func TestHandleCheckedRejectsConflictWithoutReplacingRoutes(t *testing.T) {
	endpoint := New(Config{})
	if err := endpoint.HandleChecked(http.MethodGet, "/users/{name}", func(ctx *Context) error {
		return ctx.Text(http.StatusOK, "first:"+ctx.Param("name"))
	}); err != nil {
		t.Fatalf("first HandleChecked() error = %v", err)
	}
	err := endpoint.HandleChecked(http.MethodGet, "/users/{id}", func(ctx *Context) error {
		return ctx.Text(http.StatusOK, "second")
	})
	if err == nil || !strings.Contains(err.Error(), "conflicting") {
		t.Fatalf("conflicting HandleChecked() error = %v", err)
	}

	response := executeTestRequest(endpoint, http.MethodGet, "/users/alice", "")
	if response.Code != http.StatusOK || response.Body.String() != "first:alice" {
		t.Fatalf("original route response = %d/%q", response.Code, response.Body.String())
	}
}

func TestHandleCheckedRejectsExactDuplicateWithoutReplacingRoute(t *testing.T) {
	endpoint := New(Config{})
	if err := endpoint.HandleChecked(http.MethodGet, "/health", func(ctx *Context) error {
		return ctx.Text(http.StatusOK, "first")
	}); err != nil {
		t.Fatalf("first HandleChecked() error = %v", err)
	}
	err := endpoint.HandleChecked(http.MethodGet, "/health", func(ctx *Context) error {
		return ctx.Text(http.StatusOK, "second")
	})
	if err == nil || !strings.Contains(err.Error(), "conflicting") {
		t.Fatalf("duplicate HandleChecked() error = %v", err)
	}

	response := executeTestRequest(endpoint, http.MethodGet, "/health", "")
	if response.Code != http.StatusOK || response.Body.String() != "first" {
		t.Fatalf("original route response = %d/%q, want 200/first", response.Code, response.Body.String())
	}
}

func TestHandleCheckedRejectsInvalidRegistration(t *testing.T) {
	endpoint := New(Config{})
	tests := []struct {
		name   string
		method string
		path   string
		h      Handler
		mw     []core.Middleware
	}{
		{name: "method", path: "/ok", h: func(*Context) error { return nil }},
		{name: "path", method: http.MethodGet, path: "missing-slash", h: func(*Context) error { return nil }},
		{name: "handler", method: http.MethodGet, path: "/ok"},
		{name: "middleware", method: http.MethodGet, path: "/ok", h: func(*Context) error { return nil }, mw: []core.Middleware{nil}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := endpoint.HandleChecked(test.method, test.path, test.h, test.mw...); err == nil {
				t.Fatal("HandleChecked() error = nil")
			}
		})
	}
}

func TestPatternRouteKeepsBodyLimit(t *testing.T) {
	endpoint := New(Config{MaxBodyBytes: 3})
	endpoint.Post("/users/{id}", func(ctx *Context) error {
		_, err := ctx.Body()
		return err
	})

	response := executeTestRequest(endpoint, http.MethodPost, "/users/1", "large")
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusRequestEntityTooLarge)
	}
}

func TestHandleNormalizesMethod(t *testing.T) {
	endpoint := New(Config{})
	if err := endpoint.HandleChecked("get", "/normalized", func(ctx *Context) error {
		return ctx.Text(http.StatusOK, "ok")
	}); err != nil {
		t.Fatalf("HandleChecked() error = %v", err)
	}

	response := executeTestRequest(endpoint, http.MethodGet, "/normalized", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.Code)
	}
}

func TestGroupCheckedRejectsInvalidPrefix(t *testing.T) {
	endpoint := New(Config{})
	if _, err := endpoint.GroupChecked("api"); err == nil {
		t.Fatal("GroupChecked() error = nil")
	}
	if _, err := endpoint.GroupChecked("/api/{tenant}"); err == nil {
		t.Fatal("GroupChecked() wildcard prefix error = nil")
	}
}
