package sse

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/saramand9/bignet/kpro/core"
)

func TestEndpointUseWrapsStream(t *testing.T) {
	endpoint := New(Config{Path: "/events"})
	var captured core.Invocation
	endpoint.Use(func(next core.InvocationHandler) core.InvocationHandler {
		return func(ctx core.Invocation) error {
			captured = ctx
			return next(ctx)
		}
	})
	endpoint.Handle(func(ctx *Context) error {
		return ctx.Send("message", []byte("ok"))
	})

	req := httptest.NewRequest(http.MethodGet, "http://example.com/events?cursor=1", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Test", "ok")
	recorder := httptest.NewRecorder()
	endpoint.serve(recorder, req)

	if captured == nil {
		t.Fatal("middleware was not invoked")
	}
	if captured.Transport() != core.TransportSSE {
		t.Fatalf("transport = %s, want %s", captured.Transport(), core.TransportSSE)
	}
	if captured.Operation() != "GET /events" {
		t.Fatalf("operation = %q", captured.Operation())
	}
	if captured.Headers()["X-Test"] != "ok" {
		t.Fatalf("header X-Test = %q", captured.Headers()["X-Test"])
	}
	if captured.Meta()["path"] != "/events" || captured.Meta()["query"] != "cursor=1" || captured.Meta()["stream"] != true {
		t.Fatalf("meta = %#v", captured.Meta())
	}
	if captured.RemoteAddr() == "" {
		t.Fatal("remote address is empty")
	}
	if captured.ConnID() == 0 {
		t.Fatal("conn id is zero")
	}
	if _, ok := captured.Native().(*Context); !ok {
		t.Fatalf("native = %T, want *sse.Context", captured.Native())
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if body := recorder.Body.String(); !strings.Contains(body, "event: message") || !strings.Contains(body, "data: ok") {
		t.Fatalf("body = %q", body)
	}
}

func TestEndpointUseCanRejectBeforeStream(t *testing.T) {
	endpoint := New(Config{Path: "/events"})
	errBlocked := errors.New("blocked")
	var handlerCalled bool
	endpoint.Use(func(core.InvocationHandler) core.InvocationHandler {
		return func(core.Invocation) error {
			return errBlocked
		}
	})
	endpoint.Handle(func(ctx *Context) error {
		handlerCalled = true
		return ctx.Send("message", []byte("unexpected"))
	})

	req := httptest.NewRequest(http.MethodGet, "http://example.com/events", nil)
	recorder := httptest.NewRecorder()
	endpoint.serve(recorder, req)

	if handlerCalled {
		t.Fatal("handler should not be called")
	}
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}
	if body := recorder.Body.String(); !strings.Contains(body, "blocked") {
		t.Fatalf("body = %q", body)
	}
}
