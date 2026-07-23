package grpcx

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/saramand9/bignet/kpro/core"
)

func TestServeListenerRejectsSecondStartAndStopsWithNilContext(t *testing.T) {
	server := NewServer("127.0.0.1:0")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	errCh := make(chan error, 1)
	go func() { errCh <- server.ServeListenerContext(nil, listener) }()
	deadline := time.Now().Add(time.Second)
	for server.Addr() == nil {
		if time.Now().After(deadline) {
			t.Fatal("gRPC server did not publish its listener")
		}
		time.Sleep(time.Millisecond)
	}

	second, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen second: %v", err)
	}
	defer second.Close()
	if err := server.ServeListenerContext(context.Background(), second); !errors.Is(err, core.ErrAlreadyStarted) {
		t.Fatalf("second ServeListenerContext() error = %v, want %v", err, core.ErrAlreadyStarted)
	}
	if err := server.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("first ServeListenerContext() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("gRPC server did not stop")
	}
}

func TestShutdownBeforeServeClosesStartGate(t *testing.T) {
	server := NewServer("127.0.0.1:0")
	if err := server.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	if err := server.ServeListenerContext(context.Background(), listener); !errors.Is(err, core.ErrClosed) {
		t.Fatalf("ServeListenerContext() error = %v, want %v", err, core.ErrClosed)
	}
}
