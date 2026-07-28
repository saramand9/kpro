package websocket

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/saramand9/kpro/core"
)

func TestListenAndServeReturnsCancellationShutdownDeadline(t *testing.T) {
	server := New(Config{Addr: "127.0.0.1:0"})
	server.shutdownTimeout = 50 * time.Millisecond
	serveCtx, cancelServe := context.WithCancel(context.Background())
	t.Cleanup(cancelServe)
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- server.ListenAndServe(serveCtx, func(core.Context) error { return nil })
	}()
	select {
	case <-server.Ready():
	case <-time.After(2 * time.Second):
		t.Fatal("WebSocket server did not become ready")
	}

	if !server.beginUpgrade() {
		t.Fatal("failed to model an in-flight WebSocket upgrade")
	}
	upgradePending := true
	t.Cleanup(func() {
		if upgradePending {
			server.upgrades.Done()
		}
	})
	cancelServe()
	select {
	case err := <-serveErr:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("ListenAndServe() error = %v, want deadline exceeded", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ListenAndServe() did not return after shutdown deadline")
	}

	server.upgrades.Done()
	upgradePending = false
}

func TestListenAndServeCancellationReturnsNilAfterSuccessfulShutdown(t *testing.T) {
	server := New(Config{Addr: "127.0.0.1:0"})
	server.shutdownTimeout = time.Second
	serveCtx, cancelServe := context.WithCancel(context.Background())
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- server.ListenAndServe(serveCtx, func(core.Context) error { return nil })
	}()
	select {
	case <-server.Ready():
	case <-time.After(2 * time.Second):
		cancelServe()
		t.Fatal("WebSocket server did not become ready")
	}
	cancelServe()
	select {
	case err := <-serveErr:
		if err != nil {
			t.Fatalf("ListenAndServe() error = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ListenAndServe() did not return after successful shutdown")
	}
}
