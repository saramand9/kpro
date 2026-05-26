package httpx

import (
	"net/http"
	"testing"

	"github.com/valyala/fasthttp"
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
		t.Fatal("CopyBody fast content type should not allocate compatibility header map")
	}
	if got := string(resp.Response.Header.Peek("Content-Type")); got != "text/plain" {
		t.Fatalf("content type = %q", got)
	}
	if got := string(resp.Response.Body()); got != "payload" {
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

	if resp.Response.StatusCode() != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", resp.Response.StatusCode(), http.StatusAccepted)
	}
	if got := string(resp.Response.Header.Peek("X-Test")); got != "hit" {
		t.Fatalf("x-test = %q", got)
	}
	if got := string(resp.Response.Header.Peek("Content-Type")); got != "text/plain; charset=utf-8" {
		t.Fatalf("content type = %q", got)
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

func TestRouteNotFound(t *testing.T) {
	endpoint := New(Config{})
	endpoint.Get("/exists", func(ctx *Context) error {
		return ctx.Text(http.StatusOK, "ok")
	})

	resp := executeTestRequest(endpoint, http.MethodGet, "/missing", "")

	if resp.Response.StatusCode() != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", resp.Response.StatusCode(), http.StatusNotFound)
	}
}

func TestRouteMethodNotAllowed(t *testing.T) {
	endpoint := New(Config{})
	endpoint.Get("/exists", func(ctx *Context) error {
		return ctx.Text(http.StatusOK, "ok")
	})

	resp := executeTestRequest(endpoint, http.MethodPost, "/exists", "")

	if resp.Response.StatusCode() != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", resp.Response.StatusCode(), http.StatusMethodNotAllowed)
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

func TestFastContextSkipsRequestCompatibility(t *testing.T) {
	endpoint := New(Config{FastContext: true})
	endpoint.Get("/inspect", func(ctx *Context) error {
		if ctx.Request != nil {
			t.Fatal("Request should be nil in FastContext mode")
		}
		if got := ctx.Method(); got != http.MethodGet {
			t.Fatalf("method = %q", got)
		}
		if got := ctx.Path(); got != "/inspect" {
			t.Fatalf("path = %q", got)
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
	if resp.Response.StatusCode() != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.Response.StatusCode(), http.StatusOK)
	}
}

func executeTestRequest(endpoint *Endpoint, method, path, body string) *fasthttp.RequestCtx {
	var req fasthttp.Request
	req.Header.SetMethod(method)
	req.SetRequestURI(path)
	if body != "" {
		req.SetBodyString(body)
	}
	var ctx fasthttp.RequestCtx
	ctx.Init(&req, nil, nil)
	endpoint.serve(&ctx)
	return &ctx
}
