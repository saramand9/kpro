package httpx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/echo", strings.NewReader("hello"))
	endpoint.mux.ServeHTTP(resp, req)

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
		return ctx.CopyBody(http.StatusOK, "text/plain")
	})

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/echo", strings.NewReader("payload"))
	endpoint.mux.ServeHTTP(resp, req)

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

func TestMiddlewareSnapshotRebuildsAfterUse(t *testing.T) {
	endpoint := New(Config{})
	endpoint.Get("/before", func(ctx *Context) error {
		return ctx.Text(http.StatusOK, "before")
	})
	endpoint.Use(func(next Handler) Handler {
		return func(ctx *Context) error {
			ctx.Header().Set("X-Middleware", "hit")
			return next(ctx)
		}
	})
	endpoint.Get("/after", func(ctx *Context) error {
		return ctx.Text(http.StatusOK, "after")
	})

	for _, path := range []string{"/before", "/after"} {
		resp := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		endpoint.mux.ServeHTTP(resp, req)
		if resp.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want %d", path, resp.Code, http.StatusOK)
		}
		if got := resp.Header().Get("X-Middleware"); got != "hit" {
			t.Fatalf("%s middleware header = %q", path, got)
		}
	}
}
