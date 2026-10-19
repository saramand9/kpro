package httpx

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRawResponseDoesNotSniffRequestBodyAsHTML(t *testing.T) {
	const payload = "<!DOCTYPE html><script>alert('reflected')</script>"
	for _, method := range []string{"Write", "Bytes", "Stream", "CopyBody"} {
		t.Run(method, func(t *testing.T) {
			endpoint := New(Config{})
			endpoint.Post("/echo", func(ctx *Context) error {
				body, err := ctx.Body()
				if err != nil {
					return err
				}
				switch method {
				case "Write":
					_, err = ctx.Write(body)
					return err
				case "Bytes":
					return ctx.Bytes(http.StatusOK, "", body)
				case "Stream":
					return ctx.Stream(http.StatusOK, "", strings.NewReader(string(body)))
				default:
					return ctx.CopyBody(http.StatusOK, "")
				}
			})
			server := httptest.NewServer(endpoint)
			defer server.Close()
			response, err := server.Client().Post(server.URL+"/echo", "application/octet-stream", strings.NewReader(payload))
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			if string(body) != payload {
				t.Fatalf("response body changed: %q", body)
			}
			if got := response.Header.Get("Content-Type"); got != "application/octet-stream" {
				t.Errorf("raw response content type = %q; request data must not select HTML", got)
			}
			if got := response.Header.Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("content sniffing policy = %q, want nosniff", got)
			}
		})
	}
}

func TestResponsePreservesExplicitContentType(t *testing.T) {
	endpoint := New(Config{})
	endpoint.Post("/echo", func(ctx *Context) error {
		ctx.Header().Set("Content-Type", "text/plain; charset=utf-8")
		ctx.Header().Set("X-Content-Type-Options", "invalid")
		return ctx.CopyBody(http.StatusCreated, "")
	})
	response := executeTestRequest(endpoint, http.MethodPost, "/echo", "<script>untrusted</script>")
	if got := response.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Fatalf("explicit content type changed: %q", got)
	}
	if got := response.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("sniffing policy = %q", got)
	}
	if response.Code != http.StatusCreated || response.Body.String() != "<script>untrusted</script>" {
		t.Fatalf("raw response changed: %d %q", response.Code, response.Body.String())
	}
}
