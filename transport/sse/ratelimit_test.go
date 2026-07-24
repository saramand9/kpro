// File: transport/sse/ratelimit_test.go
// Purpose: Verifies SSE event-level rate limiting behavior.

package sse

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/saramand9/kpro/core"
	"github.com/saramand9/kpro/middleware"
	"golang.org/x/time/rate"
)

func TestListenAndServeRejectsSecondStartAndStopsWithNilContext(t *testing.T) {
	addr := freeSSERateLimitAddr(t)
	endpoint := New(Config{Addr: addr})
	errCh := make(chan error, 1)
	go func() { errCh <- endpoint.ListenAndServe(nil) }()
	waitForSSERateLimitServer(t, addr, errCh)

	if err := endpoint.ListenAndServe(context.Background()); !errors.Is(err, core.ErrAlreadyStarted) {
		t.Fatalf("second ListenAndServe() error = %v, want %v", err, core.ErrAlreadyStarted)
	}
	if err := endpoint.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("first ListenAndServe() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("SSE server did not stop")
	}
}

func TestConnRateLimitAppliesToSSEEvents(t *testing.T) {
	addr := freeSSERateLimitAddr(t)
	endpoint := New(Config{Addr: addr, Path: "/events"})
	endpoint.Use(middleware.ConnRateLimit(middleware.RateLimitConfig{
		Rate:  rate.Limit(0.001),
		Burst: 2,
	}))
	var secondLimited bool
	endpoint.Handle(func(ctx *Context) error {
		if err := ctx.Send("message", []byte("one")); err != nil {
			return err
		}
		if err := ctx.Send("message", []byte("two")); err != nil {
			if err == middleware.ErrRateLimited {
				secondLimited = true
			}
			return err
		}
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- endpoint.ListenAndServe(ctx) }()
	waitForSSERateLimitServer(t, addr, errCh)
	defer func() {
		cancel()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer shutdownCancel()
		_ = endpoint.Shutdown(shutdownCtx)
		select {
		case err := <-errCh:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("sse server did not stop")
		}
	}()

	resp, err := http.Get("http://" + addr + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%q", resp.StatusCode, text)
	}
	if !strings.Contains(text, "data: one") {
		t.Fatalf("body = %q, want first event", text)
	}
	if strings.Contains(text, "data: two") {
		t.Fatalf("body = %q, second event should be limited", text)
	}
	if !secondLimited {
		t.Fatal("second event was not rate limited")
	}
}

func freeSSERateLimitAddr(t *testing.T) string {
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

func waitForSSERateLimitServer(t *testing.T, addr string, errCh <-chan error) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-errCh:
			t.Fatalf("server stopped early: %v", err)
		default:
		}
		conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server did not start on %s", addr)
}
