package sse

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEndpointServeWritesSSEHeadersAndEvents(t *testing.T) {
	endpoint := New(Config{}).Handle(func(ctx *Context) error {
		return ctx.Send("update", []byte("line1\nline2"))
	})

	resp := executeSSERequest(endpoint)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusOK)
	}
	if got := resp.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("content type = %q", got)
	}
	if got := resp.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("cache control = %q", got)
	}
	wantBody := "event: update\ndata: line1\ndata: line2\n\n"
	if !strings.Contains(resp.Body.String(), wantBody) {
		t.Fatalf("body = %q, want to contain %q", resp.Body.String(), wantBody)
	}
}

func TestEndpointServeWritesHandlerErrorEvent(t *testing.T) {
	endpoint := New(Config{}).Handle(func(*Context) error {
		return errors.New("boom")
	})

	resp := executeSSERequest(endpoint)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusOK)
	}
	wantBody := "event: error\ndata: internal error\n\n"
	if !strings.Contains(resp.Body.String(), wantBody) {
		t.Fatalf("body = %q, want to contain %q", resp.Body.String(), wantBody)
	}
}

func TestContextSendNormalizesAllEventStreamLineEndings(t *testing.T) {
	endpoint := New(Config{}).Handle(func(ctx *Context) error {
		return ctx.Send("update", []byte("cr\rcrlf\r\nlf\nlast"))
	})

	resp := executeSSERequest(endpoint)
	want := "event: update\ndata: cr\ndata: crlf\ndata: lf\ndata: last\n\n"
	if !strings.Contains(resp.Body.String(), want) {
		t.Fatalf("body = %q, want to contain %q", resp.Body.String(), want)
	}
}

func TestContextSendRejectsEventNameLineBreakInjection(t *testing.T) {
	var sendErr error
	endpoint := New(Config{}).Handle(func(ctx *Context) error {
		sendErr = ctx.Send("update\ndata: injected", []byte("safe"))
		return nil
	})

	resp := executeSSERequest(endpoint)
	if !errors.Is(sendErr, ErrInvalidEventName) {
		t.Fatalf("Send() error = %v, want %v", sendErr, ErrInvalidEventName)
	}
	if strings.Contains(resp.Body.String(), "injected") {
		t.Fatalf("invalid event name reached wire: %q", resp.Body.String())
	}
}

func TestNormalizeConfigDefaults(t *testing.T) {
	config := normalizeConfig(Config{})

	if config.Addr != DefaultAddr {
		t.Fatalf("addr = %q, want %q", config.Addr, DefaultAddr)
	}
	if config.Path != DefaultPath {
		t.Fatalf("path = %q, want %q", config.Path, DefaultPath)
	}
	if config.ShutdownTimeout != DefaultShutdownTimeout {
		t.Fatalf("shutdown timeout = %v, want %v", config.ShutdownTimeout, DefaultShutdownTimeout)
	}
}

func executeSSERequest(endpoint *Endpoint) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, endpoint.config.Path, nil)
	resp := httptest.NewRecorder()
	endpoint.serve(resp, req)
	return resp
}
