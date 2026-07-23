// File: transport/httpx/ratelimit_test.go
// Purpose: Verifies HTTP connection-level rate limiting behavior.

package httpx

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/saramand9/bignet/kpro/core"
	"github.com/saramand9/bignet/kpro/middleware"
	"golang.org/x/time/rate"
)

func TestListenAndServeRejectsSecondStartAndStopsWithNilContext(t *testing.T) {
	addr := freeHTTPRateLimitAddr(t)
	endpoint := New(Config{Addr: addr})
	errCh := make(chan error, 1)
	go func() { errCh <- endpoint.ListenAndServe(nil) }()
	waitForHTTPRateLimitServer(t, addr, errCh)

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
		t.Fatal("HTTP server did not stop")
	}
}

func TestConnRateLimitPersistsAcrossHTTPKeepAliveRequests(t *testing.T) {
	addr := freeHTTPRateLimitAddr(t)
	endpoint := New(Config{Addr: addr})
	endpoint.Use(middleware.ConnRateLimit(middleware.RateLimitConfig{
		Rate:  rate.Limit(0.001),
		Burst: 1,
	}))
	endpoint.Get("/echo", func(ctx *Context) error {
		return ctx.Text(http.StatusOK, "ok")
	})

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- endpoint.ListenAndServe(ctx) }()
	waitForHTTPRateLimitServer(t, addr, errCh)
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
			t.Fatal("http server did not stop")
		}
	}()

	client := rateLimitHTTPClient()
	defer client.CloseIdleConnections()
	body, status := doRateLimitHTTPGet(t, client, "http://"+addr+"/echo")
	if status != http.StatusOK || body != "ok" {
		t.Fatalf("first status/body = %d/%q", status, body)
	}
	body, status = doRateLimitHTTPGet(t, client, "http://"+addr+"/echo")
	if status != http.StatusInternalServerError || !strings.Contains(body, middleware.ErrRateLimited.Error()) {
		t.Fatalf("second status/body = %d/%q, want rate limit", status, body)
	}

	otherClient := rateLimitHTTPClient()
	defer otherClient.CloseIdleConnections()
	body, status = doRateLimitHTTPGet(t, otherClient, "http://"+addr+"/echo")
	if status != http.StatusOK || body != "ok" {
		t.Fatalf("other connection status/body = %d/%q", status, body)
	}
}

func rateLimitHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{
			MaxConnsPerHost:     1,
			MaxIdleConns:        1,
			MaxIdleConnsPerHost: 1,
		},
	}
}

func doRateLimitHTTPGet(t *testing.T, client *http.Client, url string) (string, int) {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body), resp.StatusCode
}

func freeHTTPRateLimitAddr(t *testing.T) string {
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

func waitForHTTPRateLimitServer(t *testing.T, addr string, errCh <-chan error) {
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
