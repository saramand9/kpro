package grpcx

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/wrapperspb"
)

type blockingShutdownGreeter struct {
	UnimplementedGreeterServer
	entered chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (g *blockingShutdownGreeter) SayHello(ctx context.Context, _ *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
	g.once.Do(func() { close(g.entered) })
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-g.release:
		return wrapperspb.String("released"), nil
	}
}

func TestListenAndServeReturnsCancellationShutdownDeadline(t *testing.T) {
	server := NewServer("127.0.0.1:0", WithShutdownTimeout(50*time.Millisecond))
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseHandler := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseHandler)
	RegisterGreeter(server, &blockingShutdownGreeter{entered: entered, release: release})

	serveCtx, cancelServe := context.WithCancel(context.Background())
	t.Cleanup(cancelServe)
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.ListenAndServe(serveCtx) }()
	select {
	case <-server.Ready():
	case <-time.After(2 * time.Second):
		t.Fatal("gRPC server did not become ready")
	}

	conn := dialTestGRPC(t, server.Addr().String())
	t.Cleanup(func() { _ = conn.Close() })
	rpcDone := make(chan struct{}, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = NewGreeterClient(conn).SayHello(ctx, wrapperspb.String("block"))
		rpcDone <- struct{}{}
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("gRPC blocking handler did not start")
	}

	cancelServe()
	select {
	case err := <-serveErr:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("ListenAndServe() error = %v, want deadline exceeded", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ListenAndServe() did not return after shutdown deadline")
	}

	select {
	case <-rpcDone:
	case <-time.After(time.Second):
		t.Fatal("forced gRPC shutdown did not cancel the active RPC")
	}
	releaseHandler()
}

func TestListenAndServeCancellationReturnsNilAfterSuccessfulShutdown(t *testing.T) {
	server := NewServer("127.0.0.1:0", WithShutdownTimeout(time.Second))
	serveCtx, cancelServe := context.WithCancel(context.Background())
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.ListenAndServe(serveCtx) }()
	select {
	case <-server.Ready():
	case <-time.After(2 * time.Second):
		cancelServe()
		t.Fatal("gRPC server did not become ready")
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
