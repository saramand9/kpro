// File: transport/httpx/http_test.go
// Purpose: Implements the native HTTP/HTTPS transport adapter.

package httpx

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/saramand9/bignet/kpro/core"
	"github.com/saramand9/bignet/kpro/middleware"
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
	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusInternalServerError)
	}
	if got := resp.Body.String(); !strings.Contains(got, "payload too large") {
		t.Fatalf("body = %q, want payload too large error", got)
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
