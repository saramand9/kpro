package fasthttpx

import (
	"bytes"
	"errors"
	"io"
	"net"
	"reflect"
	"testing"

	"github.com/valyala/fasthttp"
)

func TestContextExposesNativeHTTP11Helpers(t *testing.T) {
	endpoint := New(Config{})
	endpoint.Post("/users/{id}", func(ctx *Context) error {
		if ctx.Native() == nil {
			t.Fatal("Native() = nil")
		}
		if got := ctx.Method(); got != fasthttp.MethodPost {
			t.Fatalf("Method() = %q", got)
		}
		if got := ctx.Path(); got != "/users/42" {
			t.Fatalf("Path() = %q", got)
		}
		if got := ctx.RawQuery(); got != "active=true" {
			t.Fatalf("RawQuery() = %q", got)
		}
		if got := ctx.Host(); got != "example.test" {
			t.Fatalf("Host() = %q", got)
		}
		if got := ctx.Param("id"); got != "42" {
			t.Fatalf("Param(id) = %q", got)
		}
		if got := ctx.Query("active"); got != "true" {
			t.Fatalf("Query(active) = %q", got)
		}
		if got := ctx.RequestHeader("X-Test"); got != "value" {
			t.Fatalf("RequestHeader(X-Test) = %q", got)
		}
		var request struct {
			Name string `json:"name"`
		}
		if err := ctx.JSONBody(&request); err != nil {
			return err
		}
		if request.Name != "kpro" {
			t.Fatalf("decoded name = %q", request.Name)
		}
		ctx.SetResponseHeader("X-Route", ctx.Param("id"))
		return ctx.JSON(fasthttp.StatusCreated, map[string]string{"name": request.Name})
	})

	response := executeTestRequest(
		t,
		endpoint,
		fasthttp.MethodPost,
		"/users/42?active=true",
		[]byte(`{"name":"kpro"}`),
		map[string]string{"Content-Type": "application/json", "X-Test": "value"},
	)
	if response.StatusCode() != fasthttp.StatusCreated {
		t.Fatalf("status = %d", response.StatusCode())
	}
	if got := string(response.Header.Peek("X-Route")); got != "42" {
		t.Fatalf("X-Route = %q", got)
	}
	if got := responseBody(response); got != `{"name":"kpro"}` {
		t.Fatalf("body = %q", got)
	}
}

func TestContextBodyAndCopyBodyUseEndpointLimit(t *testing.T) {
	endpoint := New(Config{MaxBodyBytes: 4})
	endpoint.Post("/echo", func(ctx *Context) error {
		return ctx.CopyBody(fasthttp.StatusOK, "application/octet-stream")
	})

	response := executeTestRequest(t, endpoint, fasthttp.MethodPost, "/echo", []byte("test"), nil)
	if response.StatusCode() != fasthttp.StatusOK || responseBody(response) != "test" {
		t.Fatalf("response = %d/%q", response.StatusCode(), responseBody(response))
	}

	var native fasthttp.RequestCtx
	native.Request.SetBodyString("large")
	ctx := &Context{native: &native, maxBody: 4}
	_, err := ctx.Body()
	if !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("Body() error = %v, want ErrBodyTooLarge", err)
	}
	var sizeErr *BodyTooLargeError
	if !errors.As(err, &sizeErr) || sizeErr.Size != 5 || sizeErr.Limit != 4 {
		t.Fatalf("Body() typed error = %#v", sizeErr)
	}
}

func TestContextWriteAndStreamHelpers(t *testing.T) {
	endpoint := New(Config{})
	endpoint.Get("/write", func(ctx *Context) error {
		ctx.SetResponseHeader("X-Test", "one")
		ctx.AddResponseHeader("X-Test", "two")
		ctx.WriteHeader(fasthttp.StatusAccepted)
		ctx.WriteHeader(fasthttp.StatusNoContent)
		_, err := ctx.Write([]byte("accepted"))
		return err
	})
	endpoint.Get("/stream", func(ctx *Context) error {
		return ctx.Stream(fasthttp.StatusOK, "text/plain", bytes.NewBufferString("streamed"))
	})

	writeResponse := executeTestRequest(t, endpoint, fasthttp.MethodGet, "/write", nil, nil)
	if writeResponse.StatusCode() != fasthttp.StatusAccepted || responseBody(writeResponse) != "accepted" {
		t.Fatalf("write response = %d/%q", writeResponse.StatusCode(), responseBody(writeResponse))
	}
	if got := writeResponse.Header.PeekAll("X-Test"); !reflect.DeepEqual(got, [][]byte{[]byte("one"), []byte("two")}) {
		t.Fatalf("X-Test values = %q", got)
	}

	var request fasthttp.Request
	request.Header.SetMethod(fasthttp.MethodGet)
	request.SetRequestURI("/stream")
	var native fasthttp.RequestCtx
	native.Init(&request, &net.TCPAddr{}, nil)
	endpoint.ServeFastHTTP(&native)
	body, err := io.ReadAll(native.Response.BodyStream())
	if err != nil || string(body) != "streamed" {
		t.Fatalf("stream body = %q, error = %v", body, err)
	}
}
