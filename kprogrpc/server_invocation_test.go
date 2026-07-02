// File: kprogrpc/server_invocation_test.go
// Purpose: Implements or tests the gRPC integration for KPro.

package kprogrpc

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/saramand9/bignet/kpro/core"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func TestUseWrapsUnaryRequest(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	captured := make(chan core.Invocation, 1)
	srv := NewServer(ln.Addr().String())
	srv.Use(func(next core.InvocationHandler) core.InvocationHandler {
		return func(ctx core.Invocation) error {
			captured <- ctx
			return next(ctx)
		}
	})
	RegisterGreeter(srv, testGreeter{})
	go func() {
		_ = srv.ServeListener(ln)
	}()
	defer srv.Stop()

	conn := dialTestGRPC(t, ln.Addr().String())
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ctx = metadata.AppendToOutgoingContext(ctx, "x-test", "ok")
	resp, err := NewGreeterClient(conn).SayHello(ctx, wrapperspb.String("call"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Value != "hello call" {
		t.Fatalf("response = %q", resp.Value)
	}

	var invocation core.Invocation
	select {
	case invocation = <-captured:
	case <-time.After(3 * time.Second):
		t.Fatal("middleware was not invoked")
	}
	if invocation.Transport() != core.TransportGRPC {
		t.Fatalf("transport = %s, want %s", invocation.Transport(), core.TransportGRPC)
	}
	if invocation.Operation() != GreeterSayHelloFullMethod {
		t.Fatalf("operation = %q, want %q", invocation.Operation(), GreeterSayHelloFullMethod)
	}
	if invocation.Headers()["x-test"] != "ok" {
		t.Fatalf("metadata x-test = %q, want ok", invocation.Headers()["x-test"])
	}
	payload, ok := invocation.Payload().(*wrapperspb.StringValue)
	if !ok || payload.Value != "call" {
		t.Fatalf("payload = %#v, want StringValue(call)", invocation.Payload())
	}
	if invocation.RemoteAddr() == "" {
		t.Fatal("remote address is empty")
	}
	if invocation.ConnID() == 0 {
		t.Fatal("conn id is zero")
	}
	if invocation.Meta()["service"] != "helloworld.v1.Greeter" || invocation.Meta()["method"] != "SayHello" {
		t.Fatalf("meta = %#v", invocation.Meta())
	}
	if native, ok := invocation.Native().(UnaryInvocationNative); !ok || native.Request != payload || native.Info == nil {
		t.Fatalf("native = %#v", invocation.Native())
	}
}

func TestUseCanRejectUnaryBeforeHandler(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	var called atomic.Bool
	srv := NewServer(ln.Addr().String())
	srv.Use(func(core.InvocationHandler) core.InvocationHandler {
		return func(core.Invocation) error {
			return status.Error(codes.PermissionDenied, "blocked")
		}
	})
	RegisterGreeter(srv, rejectingGreeter{called: &called})
	go func() {
		_ = srv.ServeListener(ln)
	}()
	defer srv.Stop()

	conn := dialTestGRPC(t, ln.Addr().String())
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err = NewGreeterClient(conn).SayHello(ctx, wrapperspb.String("call"))
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("error code = %s, want %s: %v", status.Code(err), codes.PermissionDenied, err)
	}
	if called.Load() {
		t.Fatal("handler was called after middleware rejection")
	}
}

type rejectingGreeter struct {
	UnimplementedGreeterServer
	called *atomic.Bool
}

func (g rejectingGreeter) SayHello(context.Context, *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
	g.called.Store(true)
	return wrapperspb.String("unexpected"), nil
}

func dialTestGRPC(t *testing.T, addr string) *grpc.ClientConn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := grpc.DialContext(ctx, addr, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
	if err != nil {
		t.Fatal(err)
	}
	return conn
}
