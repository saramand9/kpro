// File: transport/httpx/invocation_test.go
// Purpose: Implements the native HTTP/HTTPS transport adapter.

package httpx

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/saramand9/kpro/core"
)

func TestEndpointUseWrapsNativeHTTPRoute(t *testing.T) {
	endpoint := New(Config{})
	var seen bool
	endpoint.Use(func(next core.InvocationHandler) core.InvocationHandler {
		return func(ctx core.Invocation) error {
			seen = true
			if got := ctx.Operation(); got != "POST /call" {
				t.Fatalf("operation = %q", got)
			}
			if ctx.Transport() != core.TransportHTTP {
				t.Fatalf("transport = %s", ctx.Transport())
			}
			payload, ok := ctx.Payload().(*BodyPayload)
			if !ok {
				t.Fatalf("payload = %T, want *httpx.BodyPayload", ctx.Payload())
			}
			if payload.ctx.bodyRead {
				t.Fatal("middleware should not eagerly read HTTP body")
			}
			if size, ok := payload.PayloadSize(); !ok || size != len("hello") {
				t.Fatalf("payload size = %d/%v, want %d/true", size, ok, len("hello"))
			}
			if got := ctx.Meta()["path"]; got != "/call" {
				t.Fatalf("path meta = %v", got)
			}
			if _, ok := ctx.Native().(*Context); !ok {
				t.Fatalf("native = %T, want *httpx.Context", ctx.Native())
			}
			return next(ctx)
		}
	})
	endpoint.Post("/call", func(ctx *Context) error {
		body, err := ctx.Body()
		if err != nil {
			return err
		}
		return ctx.Text(http.StatusOK, "body:"+string(body))
	})

	resp := executeTestRequest(endpoint, http.MethodPost, "/call", "hello")

	if !seen {
		t.Fatal("middleware was not called")
	}
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusOK)
	}
	if got := resp.Body.String(); got != "body:hello" {
		t.Fatalf("body = %q", got)
	}
}

type httpTerminalContextKey struct{}

func TestEndpointMiddlewareContextReachesNativeHTTPRoute(t *testing.T) {
	endpoint := New(Config{})
	endpoint.Use(func(next core.InvocationHandler) core.InvocationHandler {
		return func(invocation core.Invocation) error {
			ctx := context.WithValue(invocation.Context(), httpTerminalContextKey{}, "active")
			return next(core.WithInvocationContext(invocation, ctx))
		}
	})
	endpoint.Get("/context", func(ctx *Context) error {
		if got := ctx.Context().Value(httpTerminalContextKey{}); got != "active" {
			t.Fatalf("native request context value = %v, want active", got)
		}
		return ctx.Text(http.StatusOK, "ok")
	})

	resp := executeTestRequest(endpoint, http.MethodGet, "/context", "")
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusOK)
	}
}

func TestHTTPInvocationBuildsHeadersAndMetaLazily(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://example.com/lazy?q=kpro", nil)
	req.Header.Set("X-Test", "value")
	ctx := &Context{w: httptest.NewRecorder(), r: req}
	inv := newHTTPInvocation(ctx, core.TransportHTTP, "GET /lazy").(*httpInvocation)

	if inv.headersReady || inv.headers != nil {
		t.Fatal("headers should not be copied before Headers is called")
	}
	if inv.metaReady || inv.meta != nil {
		t.Fatal("meta should not be built before Meta is called")
	}
	if got := inv.Headers()["X-Test"]; got != "value" {
		t.Fatalf("header = %q, want value", got)
	}
	if got := inv.Meta()["query"]; got != "q=kpro" {
		t.Fatalf("query meta = %v, want q=kpro", got)
	}
	if !inv.headersReady || !inv.metaReady {
		t.Fatal("headers and meta should be cached after first access")
	}
}

func TestNewWithTransportUsesTransportName(t *testing.T) {
	endpoint := NewWithTransport(Config{TLS: true}, core.TransportHTTP3)
	if got := endpoint.Name(); got != string(core.TransportHTTP3) {
		t.Fatalf("name = %q, want %q", got, core.TransportHTTP3)
	}
	if err := endpoint.ListenAndServe(context.Background()); !errors.Is(err, ErrNativeAdapterRequired) {
		t.Fatalf("ListenAndServe error = %v, want %v", err, ErrNativeAdapterRequired)
	}
	if endpoint := NewWithTransport(Config{TLS: false}, core.TransportHTTPS); !endpoint.config.TLS {
		t.Fatal("HTTPS transport did not enable TLS serving")
	}
	if endpoint := NewWithTransport(Config{TLS: true}, core.TransportHTTP); endpoint.config.TLS {
		t.Fatal("HTTP transport retained conflicting TLS serving")
	}
}

func TestWithConnectionContextExposesAdapterConnectionLifecycle(t *testing.T) {
	endpoint := New(Config{})
	done := make(chan struct{})
	req := httptest.NewRequest(http.MethodGet, "http://example.com/conn", nil)
	req = req.WithContext(endpoint.WithConnectionContext(req.Context(), "peer:443", done))
	ctx := &Context{w: httptest.NewRecorder(), r: req}
	inv := newHTTPInvocation(ctx, core.TransportHTTP3, "GET /conn").(*httpInvocation)

	if inv.ConnID() == 0 {
		t.Fatal("connection id was not assigned")
	}
	if inv.RemoteAddr() != "peer:443" {
		t.Fatalf("remote = %q, want peer:443", inv.RemoteAddr())
	}
	if inv.ConnDone() != done {
		t.Fatal("connection done signal was not propagated")
	}
}

func TestHTTPMiddlewareChainCompiledOncePerRouteSnapshot(t *testing.T) {
	endpoint := New(Config{})
	builds := 0
	endpoint.Use(func(next core.InvocationHandler) core.InvocationHandler {
		builds++
		return func(inv core.Invocation) error {
			return next(inv)
		}
	})
	endpoint.Get("/compiled", func(ctx *Context) error {
		return ctx.Text(http.StatusOK, "ok")
	})

	if builds != 0 {
		t.Fatalf("middleware builds before first request = %d, want 0", builds)
	}
	for requestIndex := 0; requestIndex < 3; requestIndex++ {
		resp := executeTestRequest(endpoint, http.MethodGet, "/compiled", "")
		if resp.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", resp.Code, http.StatusOK)
		}
	}
	if builds != 1 {
		t.Fatalf("middleware builds after requests = %d, want 1", builds)
	}

	endpoint.Get("/second", func(ctx *Context) error {
		return ctx.Text(http.StatusOK, "second")
	})
	_ = executeTestRequest(endpoint, http.MethodGet, "/compiled", "")
	if builds != 1 {
		t.Fatalf("existing route middleware rebuilt after adding route: builds = %d, want 1", builds)
	}
	resp := executeTestRequest(endpoint, http.MethodGet, "/second", "")
	if resp.Code != http.StatusOK || resp.Body.String() != "second" {
		t.Fatalf("second route status/body = %d/%q", resp.Code, resp.Body.String())
	}
	if builds != 2 {
		t.Fatalf("middleware builds for two routes = %d, want 2", builds)
	}
}

func TestEndpointUseSkipsNotFoundAndMethodNotAllowed(t *testing.T) {
	endpoint := New(Config{})
	var calls int
	endpoint.Use(func(next core.InvocationHandler) core.InvocationHandler {
		return func(ctx core.Invocation) error {
			calls++
			return next(ctx)
		}
	})
	endpoint.Get("/exists", func(ctx *Context) error {
		return ctx.Text(http.StatusOK, "ok")
	})

	_ = executeTestRequest(endpoint, http.MethodGet, "/missing", "")
	_ = executeTestRequest(endpoint, http.MethodPost, "/exists", "")

	if calls != 0 {
		t.Fatalf("middleware calls = %d, want 0", calls)
	}
}

func TestEndpointUseErrorReturnsHTTPError(t *testing.T) {
	endpoint := New(Config{})
	errBlocked := errors.New("blocked")
	var handlerCalled bool
	endpoint.Use(func(core.InvocationHandler) core.InvocationHandler {
		return func(core.Invocation) error {
			return errBlocked
		}
	})
	endpoint.Get("/blocked", func(ctx *Context) error {
		handlerCalled = true
		return ctx.Text(http.StatusOK, "ok")
	})

	resp := executeTestRequest(endpoint, http.MethodGet, "/blocked", "")

	if handlerCalled {
		t.Fatal("handler should not be called")
	}
	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusInternalServerError)
	}
	if got := resp.Body.String(); got != core.ErrInternal.Error() {
		t.Fatalf("body = %q, want safe internal error", got)
	}
}
