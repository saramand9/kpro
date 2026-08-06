package grpcx

import (
	"context"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/saramand9/kpro/core"
	"github.com/saramand9/kpro/transport/proxyproto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func TestProxyProtocolPropagatesClientAddressToRPC(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	type observation struct {
		remote       string
		directRemote string
		version      uint8
		ok           bool
	}
	observed := make(chan observation, 1)
	server := NewServer(listener.Addr().String(), WithProxyProtocol(proxyproto.Config{
		Enabled:        true,
		TrustedProxies: []string{"127.0.0.1"},
	}))
	server.Use(func(next core.InvocationHandler) core.InvocationHandler {
		return func(invocation core.Invocation) error {
			info, ok := proxyproto.FromContext(invocation.Context())
			observed <- observation{
				remote:       invocation.RemoteAddr(),
				directRemote: info.DirectRemoteAddr,
				version:      info.Header.Version,
				ok:           ok,
			}
			return next(invocation)
		}
	})
	RegisterGreeter(server, testGreeter{})
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.ServeListener(listener) }()
	select {
	case <-server.Ready():
	case <-time.After(time.Second):
		server.Stop()
		t.Fatal("gRPC server did not become ready")
	}
	t.Cleanup(func() {
		server.Stop()
		select {
		case err := <-serveErr:
			if err != nil {
				t.Errorf("ServeListener: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("gRPC server did not stop")
		}
	})

	dialer := new(net.Dialer)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	connection, err := grpc.DialContext(
		ctx,
		listener.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, address string) (net.Conn, error) {
			raw, err := dialer.DialContext(ctx, "tcp", address)
			if err != nil {
				return nil, err
			}
			if _, err := io.WriteString(raw, "PROXY TCP4 203.0.113.14 198.51.100.24 45682 50051\r\n"); err != nil {
				_ = raw.Close()
				return nil, err
			}
			return raw, nil
		}),
		grpc.WithBlock(),
	)
	if err != nil {
		t.Fatalf("dial gRPC: %v", err)
	}
	defer connection.Close()
	response, err := NewGreeterClient(connection).SayHello(ctx, wrapperspb.String("kpro"))
	if err != nil {
		t.Fatalf("SayHello: %v", err)
	}
	if response.Value != "hello kpro" {
		t.Fatalf("response = %q", response.Value)
	}

	select {
	case got := <-observed:
		if !got.ok || got.version != 1 || got.remote != "203.0.113.14:45682" {
			t.Fatalf("middleware observation = %+v", got)
		}
		if !strings.HasPrefix(got.directRemote, "127.0.0.1:") {
			t.Fatalf("direct remote = %q", got.directRemote)
		}
	case <-time.After(time.Second):
		t.Fatal("gRPC middleware did not observe the call")
	}
}
