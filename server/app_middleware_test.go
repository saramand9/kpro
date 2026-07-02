// File: server/app_middleware_test.go
// Purpose: Implements or tests the high-level KPro server orchestration layer.

package server

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/saramand9/bignet/kpro/core"
	"github.com/saramand9/bignet/kpro/transport/httpx"
	"github.com/saramand9/bignet/kpro/transport/sse"
)

func TestServerUseAppliesToHTTPEndpoint(t *testing.T) {
	app := New()
	callSeen := make(chan struct{}, 1)
	middlewareErr := make(chan error, 1)
	app.Use(func(next core.InvocationHandler) core.InvocationHandler {
		return func(ctx core.Invocation) error {
			if ctx.Transport() != core.TransportHTTP {
				return recordServerTestMiddlewareError(middlewareErr, fmt.Errorf("transport = %s", ctx.Transport()))
			}
			if ctx.Operation() != "GET /middleware" {
				return recordServerTestMiddlewareError(middlewareErr, fmt.Errorf("operation = %q", ctx.Operation()))
			}
			callSeen <- struct{}{}
			return next(ctx)
		}
	})

	addr := freeServerTestTCPAddr(t)
	app.HTTP(addr).Get("/middleware", func(ctx *httpx.Context) error {
		return ctx.Text(http.StatusOK, "ok")
	})
	errCh := startServerTestApp(t, app)
	defer shutdownServerTestApp(t, app, errCh)

	respBody, status := serverTestHTTPGet(t, "http://"+addr+"/middleware", errCh)
	if status != http.StatusOK || respBody != "ok" {
		t.Fatalf("status/body = %d/%q, want %d/ok", status, respBody, http.StatusOK)
	}
	assertNoServerTestMiddlewareError(t, middlewareErr)
	waitForSignal(t, callSeen, "http middleware")
}

func TestServerUseAppliesToSSEEndpoint(t *testing.T) {
	app := New()
	callSeen := make(chan struct{}, 1)
	middlewareErr := make(chan error, 1)
	app.Use(func(next core.InvocationHandler) core.InvocationHandler {
		return func(ctx core.Invocation) error {
			if ctx.Transport() != core.TransportSSE {
				return recordServerTestMiddlewareError(middlewareErr, fmt.Errorf("transport = %s", ctx.Transport()))
			}
			if ctx.Operation() != "GET /events" {
				return recordServerTestMiddlewareError(middlewareErr, fmt.Errorf("operation = %q", ctx.Operation()))
			}
			callSeen <- struct{}{}
			return next(ctx)
		}
	})

	addr := freeServerTestTCPAddr(t)
	app.SSE(addr, "/events").Handle(func(ctx *sse.Context) error {
		return ctx.Send("message", []byte("ok"))
	})
	errCh := startServerTestApp(t, app)
	defer shutdownServerTestApp(t, app, errCh)

	respBody, status := serverTestHTTPGet(t, "http://"+addr+"/events", errCh)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if !strings.Contains(respBody, "event: message") || !strings.Contains(respBody, "data: ok") {
		t.Fatalf("body = %q", respBody)
	}
	assertNoServerTestMiddlewareError(t, middlewareErr)
	waitForSignal(t, callSeen, "sse middleware")
}

func startServerTestApp(t *testing.T, app *Server) <-chan error {
	t.Helper()
	errCh := make(chan error, 1)
	go func() {
		errCh <- app.ListenAndServe()
	}()
	return errCh
}

func shutdownServerTestApp(t *testing.T, app *Server, errCh <-chan error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := app.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server did not stop")
	}
}

func serverTestHTTPGet(t *testing.T, url string, errCh <-chan error) (string, int) {
	t.Helper()
	client := &http.Client{Timeout: 3 * time.Second}
	deadline := time.Now().Add(3 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		select {
		case err := <-errCh:
			t.Fatalf("server stopped before request completed: %v", err)
		default:
		}
		resp, err := client.Get(url)
		if err == nil {
			defer resp.Body.Close()
			body, readErr := io.ReadAll(resp.Body)
			if readErr != nil {
				t.Fatal(readErr)
			}
			return string(body), resp.StatusCode
		}
		lastErr = err
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("GET %s failed: %v", url, lastErr)
	return "", 0
}

func freeServerTestTCPAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

func recordServerTestMiddlewareError(ch chan<- error, err error) error {
	select {
	case ch <- err:
	default:
	}
	return err
}

func assertNoServerTestMiddlewareError(t *testing.T, ch <-chan error) {
	t.Helper()
	select {
	case err := <-ch:
		t.Fatal(err)
	default:
	}
}
