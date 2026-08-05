package fasthttpx

import (
	"context"
	"errors"
	"testing"

	"github.com/valyala/fasthttp"

	"github.com/saramand9/kpro/core"
	"github.com/saramand9/kpro/middleware"
)

type invocationContextKey struct{}

func TestEndpointMiddlewareReceivesLazyInvocation(t *testing.T) {
	endpoint := New(Config{})
	var seen bool
	endpoint.Use(func(next core.InvocationHandler) core.InvocationHandler {
		return func(invocation core.Invocation) error {
			seen = true
			if invocation.Operation() != "POST /call/{id}" {
				t.Fatalf("operation = %q", invocation.Operation())
			}
			if invocation.Transport() != core.TransportHTTP {
				t.Fatalf("transport = %q", invocation.Transport())
			}
			native, ok := invocation.Native().(*Context)
			if !ok || native.Native() == nil {
				t.Fatalf("native = %T", invocation.Native())
			}
			payload, ok := invocation.Payload().(*BodyPayload)
			if !ok {
				t.Fatalf("payload = %T", invocation.Payload())
			}
			if size, known := payload.PayloadSize(); !known || size != 5 {
				t.Fatalf("payload size = %d/%v", size, known)
			}
			if got := invocation.Headers()["X-Test"]; got != "value" {
				t.Fatalf("header = %q", got)
			}
			if got := invocation.Meta()["path"]; got != "/call/9" {
				t.Fatalf("path meta = %v", got)
			}
			invocation.Set("request-id", "abc")
			ctx := context.WithValue(invocation.Context(), invocationContextKey{}, "active")
			return next(core.WithInvocationContext(invocation, ctx))
		}
	})
	endpoint.Post("/call/{id}", func(ctx *Context) error {
		if got := ctx.Context().Value(invocationContextKey{}); got != "active" {
			t.Fatalf("context value = %v", got)
		}
		if got, ok := ctx.Get("request-id"); !ok || got != "abc" {
			t.Fatalf("request-id = %v/%v", got, ok)
		}
		return ctx.CopyBody(fasthttp.StatusOK, "text/plain")
	})

	response := executeTestRequest(
		t,
		endpoint,
		fasthttp.MethodPost,
		"/call/9",
		[]byte("hello"),
		map[string]string{"X-Test": "value"},
	)
	if !seen || response.StatusCode() != fasthttp.StatusOK || responseBody(response) != "hello" {
		t.Fatalf("response = seen:%v %d/%q", seen, response.StatusCode(), responseBody(response))
	}
}

func TestMaxPayloadRejectsBeforeHandler(t *testing.T) {
	endpoint := New(Config{})
	endpoint.Use(middleware.MaxPayload(3))
	handlerCalled := false
	endpoint.Post("/limited", func(ctx *Context) error {
		handlerCalled = true
		return ctx.Text(fasthttp.StatusOK, "ok")
	})

	response := executeTestRequest(t, endpoint, fasthttp.MethodPost, "/limited", []byte("large"), nil)
	if handlerCalled {
		t.Fatal("handler was called")
	}
	if response.StatusCode() != fasthttp.StatusRequestEntityTooLarge || responseBody(response) != core.ErrPayloadTooLarge.Error() {
		t.Fatalf("response = %d/%q", response.StatusCode(), responseBody(response))
	}
}

func TestHandlerErrorsUseSafeProtocolResponses(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		body   string
	}{
		{name: "rate", err: core.ErrRateLimited, status: fasthttp.StatusTooManyRequests, body: core.ErrRateLimited.Error()},
		{name: "payload", err: core.ErrPayloadTooLarge, status: fasthttp.StatusRequestEntityTooLarge, body: core.ErrPayloadTooLarge.Error()},
		{name: "overload", err: core.ErrOverloaded, status: fasthttp.StatusServiceUnavailable, body: core.ErrOverloaded.Error()},
		{name: "internal", err: errors.New("secret database detail"), status: fasthttp.StatusInternalServerError, body: core.ErrInternal.Error()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			endpoint := New(Config{})
			endpoint.Get("/error", func(*Context) error { return test.err })
			response := executeTestRequest(t, endpoint, fasthttp.MethodGet, "/error", nil, nil)
			if response.StatusCode() != test.status || responseBody(response) != test.body {
				t.Fatalf("response = %d/%q", response.StatusCode(), responseBody(response))
			}
		})
	}
}

func TestHandlerErrorAfterResponseStartedPreservesResponse(t *testing.T) {
	endpoint := New(Config{})
	endpoint.Get("/partial", func(ctx *Context) error {
		if err := ctx.Text(fasthttp.StatusAccepted, "accepted"); err != nil {
			return err
		}
		return errors.New("late failure")
	})
	response := executeTestRequest(t, endpoint, fasthttp.MethodGet, "/partial", nil, nil)
	if response.StatusCode() != fasthttp.StatusAccepted || responseBody(response) != "accepted" {
		t.Fatalf("response = %d/%q", response.StatusCode(), responseBody(response))
	}
}

type recordingLogger struct {
	calls int
}

func (l *recordingLogger) Printf(string, ...any) { l.calls++ }

func TestUnhandledPanicReturnsSafeError(t *testing.T) {
	logger := new(recordingLogger)
	endpoint := New(Config{Logger: logger})
	endpoint.Get("/panic", func(*Context) error {
		panic("secret panic")
	})
	response := executeTestRequest(t, endpoint, fasthttp.MethodGet, "/panic", nil, nil)
	if response.StatusCode() != fasthttp.StatusInternalServerError || responseBody(response) != core.ErrInternal.Error() {
		t.Fatalf("response = %d/%q", response.StatusCode(), responseBody(response))
	}
	if logger.calls != 1 {
		t.Fatalf("logger calls = %d", logger.calls)
	}
}
