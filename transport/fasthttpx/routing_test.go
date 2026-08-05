package fasthttpx

import (
	"reflect"
	"strings"
	"testing"

	"github.com/valyala/fasthttp"

	"github.com/saramand9/kpro/core"
)

func TestRouteGroupsAndMiddlewareOrder(t *testing.T) {
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
	api.Group("/v1").Get("users/{id}", func(ctx *Context) error {
		order = append(order, "handler:"+ctx.Param("id"))
		return ctx.Text(fasthttp.StatusOK, "ok")
	}, middleware("route"))

	response := executeTestRequest(t, endpoint, fasthttp.MethodGet, "/api/v1/users/7", nil, nil)
	if response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("status = %d", response.StatusCode())
	}
	want := []string{"endpoint", "group", "route", "handler:7"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("middleware order = %v, want %v", order, want)
	}
}

func TestRoutingReturnsStableNotFoundAndMethodNotAllowed(t *testing.T) {
	endpoint := New(Config{})
	middlewareCalls := 0
	endpoint.Use(func(next core.InvocationHandler) core.InvocationHandler {
		return func(invocation core.Invocation) error {
			middlewareCalls++
			return next(invocation)
		}
	})
	endpoint.Get("/exists", func(ctx *Context) error {
		return ctx.Text(fasthttp.StatusOK, "ok")
	})

	notFound := executeTestRequest(t, endpoint, fasthttp.MethodGet, "/missing", nil, nil)
	if notFound.StatusCode() != fasthttp.StatusNotFound || responseBody(notFound) != "not found" {
		t.Fatalf("not found = %d/%q", notFound.StatusCode(), responseBody(notFound))
	}
	methodNotAllowed := executeTestRequest(t, endpoint, fasthttp.MethodPost, "/exists", nil, nil)
	if methodNotAllowed.StatusCode() != fasthttp.StatusMethodNotAllowed || responseBody(methodNotAllowed) != "method not allowed" {
		t.Fatalf("method not allowed = %d/%q", methodNotAllowed.StatusCode(), responseBody(methodNotAllowed))
	}
	if middlewareCalls != 0 {
		t.Fatalf("middleware calls = %d, want 0", middlewareCalls)
	}
}

func TestHandleCheckedRejectsConflictWithoutReplacingRoutes(t *testing.T) {
	endpoint := New(Config{})
	if err := endpoint.HandleChecked(fasthttp.MethodGet, "/users/{name}", func(ctx *Context) error {
		return ctx.Text(fasthttp.StatusOK, "first:"+ctx.Param("name"))
	}); err != nil {
		t.Fatalf("first HandleChecked() error = %v", err)
	}
	err := endpoint.HandleChecked(fasthttp.MethodGet, "/users/{id}", func(ctx *Context) error {
		return ctx.Text(fasthttp.StatusOK, "second")
	})
	if err == nil || !strings.Contains(err.Error(), "conflicting") {
		t.Fatalf("conflicting HandleChecked() error = %v", err)
	}

	response := executeTestRequest(t, endpoint, fasthttp.MethodGet, "/users/alice", nil, nil)
	if response.StatusCode() != fasthttp.StatusOK || responseBody(response) != "first:alice" {
		t.Fatalf("original route = %d/%q", response.StatusCode(), responseBody(response))
	}
}

func TestHandleCheckedRejectsInvalidDefinitions(t *testing.T) {
	endpoint := New(Config{})
	handler := func(*Context) error { return nil }
	tests := []struct {
		name       string
		method     string
		path       string
		handler    Handler
		middleware []core.Middleware
	}{
		{name: "method", path: "/ok", handler: handler},
		{name: "path", method: fasthttp.MethodGet, path: "missing-slash", handler: handler},
		{name: "handler", method: fasthttp.MethodGet, path: "/ok"},
		{name: "middleware", method: fasthttp.MethodGet, path: "/ok", handler: handler, middleware: []core.Middleware{nil}},
		{name: "pattern", method: fasthttp.MethodGet, path: "/bad/{path:*}/tail", handler: handler},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := endpoint.HandleChecked(test.method, test.path, test.handler, test.middleware...); err == nil {
				t.Fatal("HandleChecked() error = nil")
			}
		})
	}
}

func TestHandleNormalizesMethod(t *testing.T) {
	endpoint := New(Config{})
	if err := endpoint.HandleChecked("get", "/normalized", func(ctx *Context) error {
		return ctx.Text(fasthttp.StatusOK, "ok")
	}); err != nil {
		t.Fatalf("HandleChecked() error = %v", err)
	}
	response := executeTestRequest(t, endpoint, fasthttp.MethodGet, "/normalized", nil, nil)
	if response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("status = %d", response.StatusCode())
	}
}
