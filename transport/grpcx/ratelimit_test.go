// File: transport/grpcx/ratelimit_test.go
// Purpose: Verifies gRPC connection-level rate limiting behavior.

package grpcx

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/saramand9/bignet/kpro/middleware"
	"golang.org/x/time/rate"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func TestConnRateLimitPersistsAcrossGRPCClientConn(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	srv := NewServer(ln.Addr().String())
	srv.Use(middleware.ConnRateLimit(middleware.RateLimitConfig{
		Rate:  rate.Limit(0.001),
		Burst: 1,
	}))
	RegisterGreeter(srv, testGreeter{})
	go func() {
		_ = srv.ServeListener(ln)
	}()
	defer srv.Stop()

	conn := dialTestGRPC(t, ln.Addr().String())
	defer conn.Close()
	client := NewGreeterClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	resp, err := client.SayHello(ctx, wrapperspb.String("one"))
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	if resp.Value != "hello one" {
		t.Fatalf("response = %q", resp.Value)
	}

	ctx, cancel = context.WithTimeout(context.Background(), 3*time.Second)
	_, err = client.SayHello(ctx, wrapperspb.String("two"))
	cancel()
	if err == nil || !strings.Contains(err.Error(), middleware.ErrRateLimited.Error()) {
		t.Fatalf("second call err = %v, want rate limit", err)
	}

	otherConn := dialTestGRPC(t, ln.Addr().String())
	defer otherConn.Close()
	ctx, cancel = context.WithTimeout(context.Background(), 3*time.Second)
	resp, err = NewGreeterClient(otherConn).SayHello(ctx, wrapperspb.String("other"))
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	if resp.Value != "hello other" {
		t.Fatalf("other response = %q", resp.Value)
	}
}
