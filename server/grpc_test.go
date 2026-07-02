package server

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/saramand9/bignet/kpro/core"
	"github.com/saramand9/bignet/kpro/kprogrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

type grpcTestGreeter struct {
	kprogrpc.UnimplementedGreeterServer
}

func (grpcTestGreeter) SayHello(_ context.Context, req *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
	return wrapperspb.String("hello " + req.Value), nil
}

func TestServerGRPCRunsWithAppLifecycle(t *testing.T) {
	app := New()
	callSeen := make(chan struct{}, 1)
	middlewareErr := make(chan error, 1)
	app.Use(func(next core.InvocationHandler) core.InvocationHandler {
		return func(ctx core.Invocation) error {
			if ctx.Transport() != core.TransportGRPC {
				return recordServerTestMiddlewareError(middlewareErr, fmt.Errorf("transport = %s", ctx.Transport()))
			}
			callSeen <- struct{}{}
			return next(ctx)
		}
	})
	endpoint := app.GRPC("127.0.0.1:0")
	kprogrpc.RegisterGreeter(endpoint, grpcTestGreeter{})

	errCh := make(chan error, 1)
	go func() {
		errCh <- app.ListenAndServe()
	}()

	addr := waitForGRPCAddr(t, endpoint)
	conn := dialServerTestGRPC(t, addr.String())
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	resp, err := kprogrpc.NewGreeterClient(conn).SayHello(ctx, wrapperspb.String("server"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Value != "hello server" {
		t.Fatalf("response = %q", resp.Value)
	}
	assertNoServerTestMiddlewareError(t, middlewareErr)
	waitForSignal(t, callSeen, "grpc middleware")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer shutdownCancel()
	if err := app.Shutdown(shutdownCtx); err != nil {
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

func waitForGRPCAddr(t *testing.T, endpoint *kprogrpc.Server) net.Addr {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if addr := endpoint.Addr(); addr != nil {
			return addr
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("grpc listener did not start")
	return nil
}

func dialServerTestGRPC(t *testing.T, addr string) *grpc.ClientConn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := grpc.DialContext(ctx, addr, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
	if err != nil {
		t.Fatal(err)
	}
	return conn
}
