package httpx

import (
	"context"
	"net/http"
	"testing"

	hertzapp "github.com/cloudwego/hertz/pkg/app"
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

	if resp.Response.StatusCode() != http.StatusCreated {
		t.Fatalf("status = %d, want %d", resp.Response.StatusCode(), http.StatusCreated)
	}
	if got := string(resp.Response.Header.Peek("Content-Type")); got != "application/octet-stream" {
		t.Fatalf("content type = %q", got)
	}
	if got := string(resp.Response.Body()); got != "hello" {
		t.Fatalf("body = %q", got)
	}
}

func TestContextCopyBody(t *testing.T) {
	endpoint := New(Config{})
	endpoint.Post("/echo", func(ctx *Context) error {
		return ctx.CopyBody(http.StatusOK, "text/plain")
	})

	resp := executeTestRequest(endpoint, http.MethodPost, "/echo", "payload")

	if resp.Response.StatusCode() != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.Response.StatusCode(), http.StatusOK)
	}
	if got := string(resp.Response.Header.Peek("Content-Type")); got != "text/plain" {
		t.Fatalf("content type = %q", got)
	}
	if got := string(resp.Response.Body()); got != "payload" {
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
		resp := executeTestRequest(endpoint, http.MethodGet, path, "")
		if resp.Response.StatusCode() != http.StatusOK {
			t.Fatalf("%s status = %d, want %d", path, resp.Response.StatusCode(), http.StatusOK)
		}
		if got := string(resp.Response.Header.Peek("X-Middleware")); got != "hit" {
			t.Fatalf("%s middleware header = %q", path, got)
		}
	}
}

func TestContextRequestCompatibility(t *testing.T) {
	endpoint := New(Config{})
	endpoint.Get("/inspect", func(ctx *Context) error {
		if ctx.Request.Method != http.MethodGet {
			t.Fatalf("method = %q", ctx.Request.Method)
		}
		if ctx.Request.URL.Path != "/inspect" {
			t.Fatalf("path = %q", ctx.Request.URL.Path)
		}
		if got := ctx.Query("name"); got != "kpro" {
			t.Fatalf("query = %q", got)
		}
		return ctx.Text(http.StatusOK, "ok")
	})

	resp := executeTestRequest(endpoint, http.MethodGet, "/inspect?name=kpro", "")
	if resp.Response.StatusCode() != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.Response.StatusCode(), http.StatusOK)
	}
}

func routePath(rawPath string) string {
	for i := range rawPath {
		if rawPath[i] == '?' {
			return rawPath[:i]
		}
	}
	return rawPath
}

func executeTestRequest(endpoint *Endpoint, method, path, body string) *hertzapp.RequestContext {
	ctx := hertzapp.NewContext(0)
	ctx.Request.SetMethod(method)
	ctx.Request.SetRequestURI(path)
	if body != "" {
		ctx.Request.SetBodyString(body)
	}
	endpoint.servePath(routePath(path))(context.Background(), ctx)
	return ctx
}
