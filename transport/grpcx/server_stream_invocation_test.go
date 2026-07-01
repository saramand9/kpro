// File: transport/grpcx/server_stream_invocation_test.go
// Purpose: Tests stream gRPC invocation middleware.

package grpcx

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/saramand9/bignet/kpro/core"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

const streamEchoFullMethod = "/test.v1.Stream/Echo"

type testStreamServer interface {
	EchoStream(grpc.ServerStream) error
}

type echoStreamService struct {
	called *atomic.Bool
}

func (s echoStreamService) EchoStream(stream grpc.ServerStream) error {
	if s.called != nil {
		s.called.Store(true)
	}
	in := new(wrapperspb.StringValue)
	if err := stream.RecvMsg(in); err != nil {
		return err
	}
	return stream.SendMsg(wrapperspb.String("echo " + in.Value))
}

func registerTestStream(s grpc.ServiceRegistrar, srv testStreamServer) {
	s.RegisterService(&testStreamServiceDesc, srv)
}

var testStreamServiceDesc = grpc.ServiceDesc{
	ServiceName: "test.v1.Stream",
	HandlerType: (*testStreamServer)(nil),
	Streams: []grpc.StreamDesc{
		{
			StreamName:    "Echo",
			Handler:       testStreamEchoHandler,
			ServerStreams: true,
			ClientStreams: true,
		},
	},
	Metadata: "test.proto",
}

func testStreamEchoHandler(srv any, stream grpc.ServerStream) error {
	return srv.(testStreamServer).EchoStream(stream)
}

func TestUseWrapsStreamRequest(t *testing.T) {
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
	registerTestStream(srv, echoStreamService{})
	go func() {
		_ = srv.ServeListener(ln)
	}()
	defer srv.Stop()

	conn := dialTestGRPC(t, ln.Addr().String())
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ctx = metadata.AppendToOutgoingContext(ctx, "x-test", "ok")
	stream, err := conn.NewStream(ctx, &testStreamServiceDesc.Streams[0], streamEchoFullMethod)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.SendMsg(wrapperspb.String("stream")); err != nil {
		t.Fatal(err)
	}
	if err := stream.CloseSend(); err != nil {
		t.Fatal(err)
	}
	out := new(wrapperspb.StringValue)
	if err := stream.RecvMsg(out); err != nil {
		t.Fatal(err)
	}
	if out.Value != "echo stream" {
		t.Fatalf("response = %q", out.Value)
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
	if invocation.Operation() != streamEchoFullMethod {
		t.Fatalf("operation = %q, want %q", invocation.Operation(), streamEchoFullMethod)
	}
	if invocation.Headers()["x-test"] != "ok" {
		t.Fatalf("metadata x-test = %q, want ok", invocation.Headers()["x-test"])
	}
	if invocation.Payload() != nil {
		t.Fatalf("payload = %#v, want nil for stream invocation", invocation.Payload())
	}
	if invocation.Meta()["service"] != "test.v1.Stream" || invocation.Meta()["method"] != "Echo" {
		t.Fatalf("meta = %#v", invocation.Meta())
	}
	if invocation.Meta()["stream"] != true || invocation.Meta()["client_stream"] != true || invocation.Meta()["server_stream"] != true {
		t.Fatalf("stream meta = %#v", invocation.Meta())
	}
	if native, ok := invocation.Native().(StreamInvocationNative); !ok || native.Stream == nil || native.Info == nil {
		t.Fatalf("native = %#v", invocation.Native())
	}
}

func TestUseCanRejectStreamBeforeHandler(t *testing.T) {
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
	registerTestStream(srv, echoStreamService{called: &called})
	go func() {
		_ = srv.ServeListener(ln)
	}()
	defer srv.Stop()

	conn := dialTestGRPC(t, ln.Addr().String())
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stream, err := conn.NewStream(ctx, &testStreamServiceDesc.Streams[0], streamEchoFullMethod)
	if err == nil {
		err = stream.SendMsg(wrapperspb.String("blocked"))
	}
	if err == nil {
		err = stream.CloseSend()
	}
	if err == nil {
		err = stream.RecvMsg(new(wrapperspb.StringValue))
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("error code = %s, want %s: %v", status.Code(err), codes.PermissionDenied, err)
	}
	if called.Load() {
		t.Fatal("handler was called after middleware rejection")
	}
}
