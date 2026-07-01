package httpx

import (
	"errors"
	"net/http"
	"testing"

	"github.com/saramand9/bignet/kpro/core"
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
			payload, ok := ctx.Payload().([]byte)
			if !ok || string(payload) != "hello" {
				t.Fatalf("payload = %T %q", ctx.Payload(), ctx.Payload())
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
	if got := resp.Body.String(); got != "blocked" {
		t.Fatalf("body = %q", got)
	}
}
