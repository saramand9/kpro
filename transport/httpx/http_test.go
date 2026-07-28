// File: transport/httpx/http_test.go
// Purpose: Implements the native HTTP/HTTPS transport adapter.

package httpx

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/saramand9/kpro/core"
	"github.com/saramand9/kpro/middleware"
)

func TestContextBytes(t *testing.T) {
	endpoint := New(Config{})
	endpoint.Post("/echo", func(ctx *Context) error {
		body, err := ctx.Body()
		if err != nil {
			return err
		}
		return ctx.Bytes(http.StatusCreated, "application/octet-stream", body)
	})

	resp := executeTestRequest(endpoint, http.MethodPost, "/echo", "hello")

	if resp.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusCreated)
	}
	if got := resp.Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Fatalf("content type = %q", got)
	}
	if got := resp.Body.String(); got != "hello" {
		t.Fatalf("body = %q", got)
	}
}

func TestHTTPConfigUsesSafeZeroValueDefaults(t *testing.T) {
	endpoint := New(Config{})
	server := endpoint.buildServer()
	if server.ReadHeaderTimeout != DefaultReadHeaderTimeout {
		t.Fatalf("ReadHeaderTimeout = %v, want %v", server.ReadHeaderTimeout, DefaultReadHeaderTimeout)
	}
	if server.ReadTimeout != DefaultReadTimeout {
		t.Fatalf("ReadTimeout = %v, want %v", server.ReadTimeout, DefaultReadTimeout)
	}
	if server.WriteTimeout != DefaultWriteTimeout {
		t.Fatalf("WriteTimeout = %v, want %v", server.WriteTimeout, DefaultWriteTimeout)
	}
	if server.IdleTimeout != DefaultIdleTimeout {
		t.Fatalf("IdleTimeout = %v, want %v", server.IdleTimeout, DefaultIdleTimeout)
	}
	if server.MaxHeaderBytes != DefaultMaxHeaderBytes {
		t.Fatalf("MaxHeaderBytes = %d, want %d", server.MaxHeaderBytes, DefaultMaxHeaderBytes)
	}
	if endpoint.config.MaxBodyBytes != DefaultMaxBodyBytes {
		t.Fatalf("MaxBodyBytes = %d, want %d", endpoint.config.MaxBodyBytes, DefaultMaxBodyBytes)
	}
}

func TestHTTPConfigRejectsNegativeHeaderLimits(t *testing.T) {
	err := New(Config{ReadHeaderTimeout: -1, MaxHeaderBytes: -1}).Validate()
	if err == nil || !strings.Contains(err.Error(), "ReadHeaderTimeout") || !strings.Contains(err.Error(), "MaxHeaderBytes") {
		t.Fatalf("Validate() error = %v, want both header limit errors", err)
	}
}

func TestContextCopyBody(t *testing.T) {
	endpoint := New(Config{})
	endpoint.Post("/echo", func(ctx *Context) error {
		body, err := ctx.Body()
		if err != nil {
			return err
		}
		if err := ctx.CopyBody(http.StatusOK, "text/plain"); err != nil {
			return err
		}
		body[0] = 'X'
		return nil
	})

	resp := executeTestRequest(endpoint, http.MethodPost, "/echo", "payload")

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusOK)
	}
	if got := resp.Header().Get("Content-Type"); got != "text/plain" {
		t.Fatalf("content type = %q", got)
	}
	if got := resp.Body.String(); got != "payload" {
		t.Fatalf("body = %q", got)
	}
}

func TestContextCopyBodyFastContentTypeSkipsHeaderMap(t *testing.T) {
	endpoint := New(Config{})
	allocatedHeaderMap := false
	endpoint.Post("/echo", func(ctx *Context) error {
		if err := ctx.CopyBody(http.StatusOK, "text/plain"); err != nil {
			return err
		}
		allocatedHeaderMap = ctx.header != nil
		return nil
	})

	resp := executeTestRequest(endpoint, http.MethodPost, "/echo", "payload")

	if allocatedHeaderMap {
		t.Fatal("CopyBody should not allocate compatibility header map")
	}
	if got := resp.Header().Get("Content-Type"); got != "text/plain" {
		t.Fatalf("content type = %q", got)
	}
	if got := resp.Body.String(); got != "payload" {
		t.Fatalf("body = %q", got)
	}
}

func TestContextBodyRejectsOversizedRequest(t *testing.T) {
	endpoint := New(Config{MaxBodyBytes: 4})
	endpoint.Post("/echo", func(ctx *Context) error {
		_, err := ctx.Body()
		return err
	})

	resp := executeTestRequest(endpoint, http.MethodPost, "/echo", "hello")

	if resp.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d; body=%q", resp.Code, http.StatusRequestEntityTooLarge, resp.Body.String())
	}
}

func TestContextRequestBodyCannotBypassEndpointLimit(t *testing.T) {
	endpoint := New(Config{MaxBodyBytes: 4})
	var readErr error
	endpoint.Post("/echo", func(ctx *Context) error {
		_, readErr = io.ReadAll(ctx.Request().Body)
		return readErr
	})

	resp := executeTestRequest(endpoint, http.MethodPost, "/echo", "hello")

	if resp.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d; body=%q", resp.Code, http.StatusRequestEntityTooLarge, resp.Body.String())
	}
	var maxBytesErr *http.MaxBytesError
	if !errors.As(readErr, &maxBytesErr) {
		t.Fatalf("Request().Body error = %T %v, want *http.MaxBytesError", readErr, readErr)
	}
}

func TestContextBodySharesLimitAfterNativePartialRead(t *testing.T) {
	endpoint := New(Config{MaxBodyBytes: 4})
	var bodyErr error
	endpoint.Post("/echo", func(ctx *Context) error {
		prefix := make([]byte, 3)
		if _, err := io.ReadFull(ctx.Request().Body, prefix); err != nil {
			return err
		}
		_, bodyErr = ctx.Body()
		return bodyErr
	})

	resp := executeTestRequest(endpoint, http.MethodPost, "/echo", "abcdef")

	if resp.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d; body=%q", resp.Code, http.StatusRequestEntityTooLarge, resp.Body.String())
	}
	var maxBytesErr *http.MaxBytesError
	if !errors.As(bodyErr, &maxBytesErr) {
		t.Fatalf("Body() error = %T %v, want *http.MaxBytesError", bodyErr, bodyErr)
	}
}

func TestContextBodyCachesChunkedOversizeErrorAcrossLengthAndBodyCalls(t *testing.T) {
	endpoint := New(Config{MaxBodyBytes: 4})
	var firstErr error
	endpoint.Post("/echo", func(ctx *Context) error {
		if length := ctx.ContentLength(); length != 0 {
			t.Fatalf("content length = %d, want 0 after oversized chunked body", length)
		}
		_, firstErr = ctx.Body()
		if firstErr == nil {
			t.Fatal("first Body() error = nil, want oversized body error")
		}
		_, secondErr := ctx.Body()
		if secondErr != firstErr {
			t.Fatalf("second Body() error = %v, want cached %v", secondErr, firstErr)
		}
		return secondErr
	})

	req := httptest.NewRequest(http.MethodPost, "/echo", strings.NewReader("toolong"))
	req.ContentLength = -1
	resp := httptest.NewRecorder()
	endpoint.ServeHTTP(resp, req)

	if resp.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d; body=%q", resp.Code, http.StatusRequestEntityTooLarge, resp.Body.String())
	}
	var maxBytesErr *http.MaxBytesError
	if !errors.As(firstErr, &maxBytesErr) {
		t.Fatalf("Body() error = %T %v, want *http.MaxBytesError", firstErr, firstErr)
	}
}

func TestReadyClosesAfterHTTPListenerBinds(t *testing.T) {
	endpoint := New(Config{Addr: "127.0.0.1:0"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- endpoint.ListenAndServe(ctx) }()

	select {
	case <-endpoint.Ready():
	case <-time.After(time.Second):
		t.Fatal("Ready() did not close after listener bind")
	}
	endpoint.mu.RLock()
	listener := endpoint.listener
	endpoint.mu.RUnlock()
	if listener == nil {
		t.Fatal("Ready() closed without a published listener")
	}
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("ListenAndServe() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("HTTP endpoint did not stop")
	}
}

func TestReadyStaysOpenOnHTTPStartupFailureAndShutdownBeforeStart(t *testing.T) {
	invalid := New(Config{Addr: "invalid"})
	if err := invalid.ListenAndServe(context.Background()); err == nil {
		t.Fatal("ListenAndServe() error = nil, want invalid address error")
	}
	select {
	case <-invalid.Ready():
		t.Fatal("Ready() closed after startup failure")
	default:
	}

	closed := New(Config{})
	if err := closed.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	select {
	case <-closed.Ready():
		t.Fatal("Ready() closed after shutdown-before-start")
	default:
	}
}

func TestContextJSONBody(t *testing.T) {
	endpoint := New(Config{})
	endpoint.Post("/json", func(ctx *Context) error {
		var request struct {
			Name string `json:"name"`
		}
		if err := ctx.JSONBody(&request); err != nil {
			return err
		}
		return ctx.JSON(http.StatusOK, request)
	})

	resp := executeTestRequest(endpoint, http.MethodPost, "/json", `{"name":"Ada"}`)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusOK)
	}
	if got := resp.Body.String(); !strings.Contains(got, `"name":"Ada"`) {
		t.Fatalf("body = %q", got)
	}
}

func TestContextContentTypeKeepsExistingHeaderMap(t *testing.T) {
	endpoint := New(Config{})
	endpoint.Get("/text", func(ctx *Context) error {
		ctx.Header().Set("X-Test", "hit")
		return ctx.Text(http.StatusAccepted, "ok")
	})

	resp := executeTestRequest(endpoint, http.MethodGet, "/text", "")

	if resp.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusAccepted)
	}
	if got := resp.Header().Get("X-Test"); got != "hit" {
		t.Fatalf("x-test = %q", got)
	}
	if got := resp.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Fatalf("content type = %q", got)
	}
}

func TestHandlerErrorAfterResponseStartedDoesNotAppendBody(t *testing.T) {
	endpoint := New(Config{})
	endpoint.Get("/partial", func(ctx *Context) error {
		if err := ctx.Text(http.StatusAccepted, "accepted"); err != nil {
			return err
		}
		return errors.New("late failure")
	})

	resp := executeTestRequest(endpoint, http.MethodGet, "/partial", "")

	if resp.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusAccepted)
	}
	if got := resp.Body.String(); got != "accepted" {
		t.Fatalf("body = %q, want response body without late error text", got)
	}
}

func TestHandlerErrorsUseProtocolStatusAndSafeBodies(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		body   string
	}{
		{name: "rate limited", err: core.ErrRateLimited, status: http.StatusTooManyRequests, body: "rate limit exceeded"},
		{name: "payload too large", err: core.ErrPayloadTooLarge, status: http.StatusRequestEntityTooLarge, body: "payload too large"},
		{name: "overloaded", err: core.ErrOverloaded, status: http.StatusServiceUnavailable, body: "service overloaded"},
		{name: "internal", err: core.ErrRecovered, status: http.StatusInternalServerError, body: "internal error"},
		{name: "unknown", err: errors.New("database password leaked"), status: http.StatusInternalServerError, body: "internal error"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			endpoint := New(Config{})
			endpoint.Get("/failure", func(*Context) error { return test.err })
			response := executeTestRequest(endpoint, http.MethodGet, "/failure", "")
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d", response.Code, test.status)
			}
			if body := response.Body.String(); body != test.body {
				t.Fatalf("body = %q, want %q", body, test.body)
			}
		})
	}
}

func TestMiddlewareSnapshotRebuildsAfterUse(t *testing.T) {
	endpoint := New(Config{})
	endpoint.Get("/before", func(ctx *Context) error {
		return ctx.Text(http.StatusOK, "before")
	})
	endpoint.Use(func(next core.InvocationHandler) core.InvocationHandler {
		return func(inv core.Invocation) error {
			ctx := inv.Native().(*Context)
			ctx.Header().Set("X-Middleware", "hit")
			return next(inv)
		}
	})
	endpoint.Get("/after", func(ctx *Context) error {
		return ctx.Text(http.StatusOK, "after")
	})

	for _, path := range []string{"/before", "/after"} {
		resp := executeTestRequest(endpoint, http.MethodGet, path, "")
		if resp.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want %d", path, resp.Code, http.StatusOK)
		}
		if got := resp.Header().Get("X-Middleware"); got != "hit" {
			t.Fatalf("%s middleware header = %q", path, got)
		}
	}
}

func TestMaxPayloadRejectsByContentLengthWithoutReadingBody(t *testing.T) {
	endpoint := New(Config{})
	handlerCalled := false
	endpoint.Use(middleware.MaxPayload(3))
	endpoint.Post("/limited", func(ctx *Context) error {
		handlerCalled = true
		return ctx.Text(http.StatusOK, "unexpected")
	})

	resp := executeTestRequest(endpoint, http.MethodPost, "/limited", "toolong")

	if handlerCalled {
		t.Fatal("handler should not be called")
	}
	if resp.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusRequestEntityTooLarge)
	}
	if got := resp.Body.String(); !strings.Contains(got, "payload too large") {
		t.Fatalf("body = %q, want payload too large error", got)
	}
}

func TestBodyPayloadLimitReturnsTypedError(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/limited", strings.NewReader("toolong"))
	ctx := &Context{w: httptest.NewRecorder(), r: req}

	err := NewBodyPayload(ctx).LimitPayload(3)
	if !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("error = %v, want errors.Is ErrBodyTooLarge", err)
	}
	var sizeErr *BodyTooLargeError
	if !errors.As(err, &sizeErr) {
		t.Fatalf("error type = %T, want *BodyTooLargeError", err)
	}
	if sizeErr.Size != len("toolong") || sizeErr.Limit != 3 {
		t.Fatalf("body size error = %+v", sizeErr)
	}
}

func TestRouteNotFound(t *testing.T) {
	endpoint := New(Config{})
	endpoint.Get("/exists", func(ctx *Context) error {
		return ctx.Text(http.StatusOK, "ok")
	})

	resp := executeTestRequest(endpoint, http.MethodGet, "/missing", "")

	if resp.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusNotFound)
	}
}

func TestRouteMethodNotAllowed(t *testing.T) {
	endpoint := New(Config{})
	endpoint.Get("/exists", func(ctx *Context) error {
		return ctx.Text(http.StatusOK, "ok")
	})

	resp := executeTestRequest(endpoint, http.MethodPost, "/exists", "")

	if resp.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusMethodNotAllowed)
	}
}

func TestContextFastAccessors(t *testing.T) {
	endpoint := New(Config{})
	endpoint.Get("/inspect", func(ctx *Context) error {
		if got := ctx.Method(); got != http.MethodGet {
			t.Fatalf("method = %q", got)
		}
		if got := string(ctx.MethodBytes()); got != http.MethodGet {
			t.Fatalf("method bytes = %q", got)
		}
		if got := ctx.Path(); got != "/inspect" {
			t.Fatalf("path = %q", got)
		}
		if got := string(ctx.PathBytes()); got != "/inspect" {
			t.Fatalf("path bytes = %q", got)
		}
		if got := ctx.RawQuery(); got != "name=kpro" {
			t.Fatalf("raw query = %q", got)
		}
		if got := ctx.Query("name"); got != "kpro" {
			t.Fatalf("query = %q", got)
		}
		return ctx.Text(http.StatusOK, "ok")
	})

	resp := executeTestRequest(endpoint, http.MethodGet, "/inspect?name=kpro", "")
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusOK)
	}
}

func executeTestRequest(endpoint *Endpoint, method, path, body string) *httptest.ResponseRecorder {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	recorder := httptest.NewRecorder()
	endpoint.serve(recorder, req)
	return recorder
}
