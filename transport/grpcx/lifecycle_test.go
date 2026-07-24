package grpcx

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/saramand9/kpro/core"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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
	select {
	case <-server.Ready():
	case <-time.After(time.Second):
		t.Fatal("Ready did not close after listener publish")
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

func TestDefaultErrorMapperHidesUnknownDetails(t *testing.T) {
	err := defaultErrorMapper(errors.New("database password leaked"))
	if got := status.Code(err); got != codes.Internal {
		t.Fatalf("code = %v, want %v", got, codes.Internal)
	}
	if got := status.Convert(err).Message(); got != "internal error" {
		t.Fatalf("message = %q, want safe internal error", got)
	}
}

func TestDefaultErrorMapperHidesWrappedStatusAndContextDetails(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantCode    codes.Code
		wantMessage string
	}{
		{
			name:        "wrapped status",
			err:         fmt.Errorf("database password leaked: %w", status.Error(codes.InvalidArgument, "public detail")),
			wantCode:    codes.Internal,
			wantMessage: "internal error",
		},
		{
			name:        "wrapped canceled",
			err:         fmt.Errorf("database password leaked: %w", context.Canceled),
			wantCode:    codes.Canceled,
			wantMessage: context.Canceled.Error(),
		},
		{
			name:        "wrapped deadline",
			err:         fmt.Errorf("database password leaked: %w", context.DeadlineExceeded),
			wantCode:    codes.DeadlineExceeded,
			wantMessage: context.DeadlineExceeded.Error(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mapped := status.Convert(defaultErrorMapper(tt.err))
			if mapped.Code() != tt.wantCode {
				t.Fatalf("code = %v, want %v", mapped.Code(), tt.wantCode)
			}
			if mapped.Message() != tt.wantMessage {
				t.Fatalf("message = %q, want %q", mapped.Message(), tt.wantMessage)
			}
			if strings.Contains(mapped.Message(), "password") {
				t.Fatalf("mapped message leaks internal detail: %q", mapped.Message())
			}
		})
	}
}

func TestDefaultErrorMapperPreservesDirectExplicitStatus(t *testing.T) {
	mapped := status.Convert(defaultErrorMapper(status.Error(codes.InvalidArgument, "public detail")))
	if mapped.Code() != codes.InvalidArgument || mapped.Message() != "public detail" {
		t.Fatalf("mapped status = %v/%q, want InvalidArgument/public detail", mapped.Code(), mapped.Message())
	}
}

func TestUnaryInterceptorMapsErrorsWithoutMiddleware(t *testing.T) {
	server := NewServer(":0")
	_, err := server.unaryCallInterceptor()(
		context.Background(),
		struct{}{},
		&grpc.UnaryServerInfo{FullMethod: "/test.v1.Service/Unary"},
		func(context.Context, any) (any, error) {
			return nil, errors.New("database password leaked")
		},
	)
	if got := status.Code(err); got != codes.Internal {
		t.Fatalf("code = %v, want %v", got, codes.Internal)
	}
	if got := status.Convert(err).Message(); got != "internal error" {
		t.Fatalf("message = %q, want safe internal error", got)
	}
}

func TestStreamInterceptorMapsErrorsWithoutMiddleware(t *testing.T) {
	server := NewServer(":0")
	err := server.streamCallInterceptor()(
		struct{}{},
		&contextOnlyServerStream{ctx: context.Background()},
		&grpc.StreamServerInfo{FullMethod: "/test.v1.Service/Stream", IsServerStream: true},
		func(any, grpc.ServerStream) error {
			return errors.New("database password leaked")
		},
	)
	if got := status.Code(err); got != codes.Internal {
		t.Fatalf("code = %v, want %v", got, codes.Internal)
	}
	if got := status.Convert(err).Message(); got != "internal error" {
		t.Fatalf("message = %q, want safe internal error", got)
	}
}

func TestCustomErrorMapperRunsWithoutMiddleware(t *testing.T) {
	source := errors.New("source failure")
	calls := 0
	server := NewServer(":0", WithErrorMapper(func(err error) error {
		calls++
		if !errors.Is(err, source) {
			t.Fatalf("mapped error = %v, want source failure", err)
		}
		return status.Error(codes.FailedPrecondition, "mapped")
	}))

	_, unaryErr := server.unaryCallInterceptor()(
		context.Background(),
		struct{}{},
		&grpc.UnaryServerInfo{FullMethod: "/test.v1.Service/Unary"},
		func(context.Context, any) (any, error) { return nil, source },
	)
	streamErr := server.streamCallInterceptor()(
		struct{}{},
		&contextOnlyServerStream{ctx: context.Background()},
		&grpc.StreamServerInfo{FullMethod: "/test.v1.Service/Stream", IsServerStream: true},
		func(any, grpc.ServerStream) error { return source },
	)
	for name, err := range map[string]error{"unary": unaryErr, "stream": streamErr} {
		if got := status.Code(err); got != codes.FailedPrecondition {
			t.Errorf("%s code = %v, want %v", name, got, codes.FailedPrecondition)
		}
		if got := status.Convert(err).Message(); got != "mapped" {
			t.Errorf("%s message = %q, want mapped", name, got)
		}
	}
	if calls != 2 {
		t.Fatalf("mapper calls = %d, want 2", calls)
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
