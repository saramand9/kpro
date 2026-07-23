package zapx

import (
	"context"
	"errors"
	"testing"

	"github.com/saramand9/bignet/kpro/core"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestMiddlewareLogsStableInvocationFields(t *testing.T) {
	coreObserver, logs := observer.New(zapcore.DebugLevel)
	logger := newObservedLogger(coreObserver)
	wantErr := errors.New("failed")
	handler := Middleware(logger)(func(core.Invocation) error { return wantErr })
	invocation := core.NewInvocation(core.InvocationOptions{
		Context:   context.Background(),
		Transport: core.TransportGRPC,
		Operation: "/example.Service/Call",
		ConnID:    9,
	})
	if err := handler(invocation); !errors.Is(err, wantErr) {
		t.Fatalf("handler error = %v", err)
	}
	entries := logs.All()
	if len(entries) != 1 {
		t.Fatalf("log entries = %d, want 1", len(entries))
	}
	fields := entries[0].ContextMap()
	if fields["transport"] != "grpc" || fields["operation"] != "/example.Service/Call" {
		t.Fatalf("log fields = %#v", fields)
	}
	if fields["conn_id"] != uint64(9) {
		t.Fatalf("conn_id = %#v", fields["conn_id"])
	}
}
