// File: transport/grpcx/helloworld_test.go
// Purpose: Tests the generated-style example Greeter binding.

package grpcx

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

type testGreeter struct {
	UnimplementedGreeterServer
}

func (testGreeter) SayHello(_ context.Context, req *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
	return wrapperspb.String("hello " + req.Value), nil
}

func TestRegisterGreeter(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	srv := NewServer(ln.Addr().String())
	RegisterGreeter(srv, testGreeter{})
	go func() {
		_ = srv.ServeListener(ln)
	}()
	defer srv.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	conn, err := grpc.DialContext(ctx, ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	resp, err := NewGreeterClient(conn).SayHello(ctx, wrapperspb.String("kpro"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Value != "hello kpro" {
		t.Fatalf("unexpected response: %q", resp.Value)
	}
}
