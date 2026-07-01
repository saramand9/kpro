package test

import (
	"bytes"
	"context"
	"net"
	"testing"
	"time"

	"github.com/saramand9/bignet/kpro/kprogrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

type grpcGreeter struct {
	kprogrpc.UnimplementedGreeterServer
}

func (grpcGreeter) SayHello(_ context.Context, req *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
	return wrapperspb.String("hello " + req.Value), nil
}

func (grpcGreeter) SayGoodbye(_ context.Context, req *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
	return wrapperspb.String("goodbye " + req.Value), nil
}

func (grpcGreeter) GetNameLength(_ context.Context, req *wrapperspb.StringValue) (*wrapperspb.Int32Value, error) {
	return wrapperspb.Int32(int32(len(req.Value))), nil
}

func (grpcGreeter) EchoBytes(_ context.Context, req *wrapperspb.BytesValue) (*wrapperspb.BytesValue, error) {
	return wrapperspb.Bytes(append([]byte(nil), req.Value...)), nil
}

func TestKProGRPCRegisterGreeterWithStandardClient(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	srv := kprogrpc.NewServer(ln.Addr().String())
	kprogrpc.RegisterGreeter(srv, grpcGreeter{})
	go func() {
		_ = srv.ServeListener(ln)
	}()
	defer srv.Stop()

	conn := dialGRPC(t, ln.Addr().String())
	defer conn.Close()

	assertGreeterRPCs(t, kprogrpc.NewGreeterClient(conn), "integration")
}

func TestStandardGRPCServerWithKProGRPCClient(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	srv := grpc.NewServer()
	kprogrpc.RegisterGreeter(srv, grpcGreeter{})
	go func() {
		_ = srv.Serve(ln)
	}()
	defer srv.Stop()

	conn := dialGRPC(t, ln.Addr().String())
	defer conn.Close()

	assertGreeterRPCs(t, kprogrpc.NewGreeterClient(conn), "standard")
}

func dialGRPC(t *testing.T, addr string) *grpc.ClientConn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	conn, err := grpc.DialContext(ctx, addr, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

func assertGreeterRPCs(t *testing.T, client kprogrpc.GreeterClient, name string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	hello, err := client.SayHello(ctx, wrapperspb.String(name))
	if err != nil {
		t.Fatal(err)
	}
	if hello.Value != "hello "+name {
		t.Fatalf("SayHello response = %q", hello.Value)
	}

	goodbye, err := client.SayGoodbye(ctx, wrapperspb.String(name))
	if err != nil {
		t.Fatal(err)
	}
	if goodbye.Value != "goodbye "+name {
		t.Fatalf("SayGoodbye response = %q", goodbye.Value)
	}

	length, err := client.GetNameLength(ctx, wrapperspb.String(name))
	if err != nil {
		t.Fatal(err)
	}
	if length.Value != int32(len(name)) {
		t.Fatalf("GetNameLength response = %d", length.Value)
	}

	payload := []byte{0, 1, 2, 3, 255}
	echo, err := client.EchoBytes(ctx, wrapperspb.Bytes(payload))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(echo.Value, payload) {
		t.Fatalf("EchoBytes response = %v", echo.Value)
	}
}
