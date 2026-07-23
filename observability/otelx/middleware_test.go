package otelx

import (
	"context"
	"errors"
	"testing"

	"github.com/saramand9/bignet/kpro/core"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestMiddlewarePropagatesSpanAndRecordsError(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	defer func() { _ = provider.Shutdown(context.Background()) }()
	wantErr := errors.New("handler failed")
	invocation := core.NewInvocation(core.InvocationOptions{
		Context:   context.Background(),
		Transport: core.TransportTCP,
		Operation: core.MessageOperation(42),
		ConnID:    7,
		Remote:    "127.0.0.1:9000",
	})
	handler := Middleware(Config{TracerProvider: provider})(func(inv core.Invocation) error {
		if !trace.SpanFromContext(inv.Context()).SpanContext().IsValid() {
			t.Fatal("downstream invocation did not receive the active span")
		}
		return wantErr
	})
	if err := handler(invocation); !errors.Is(err, wantErr) {
		t.Fatalf("handler error = %v, want %v", err, wantErr)
	}
	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("exported spans = %d, want 1", len(spans))
	}
	if spans[0].Name != core.MessageOperation(42) {
		t.Fatalf("span name = %q", spans[0].Name)
	}
	if spans[0].Status.Code.String() != "Error" {
		t.Fatalf("span status = %s, want Error", spans[0].Status.Code)
	}
}

func TestMiddlewareExtractsCanonicalHTTPTraceparent(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	defer func() { _ = provider.Shutdown(context.Background()) }()
	invocation := core.NewInvocation(core.InvocationOptions{
		Context:   context.Background(),
		Transport: core.TransportHTTP,
		Operation: "GET /trace",
		Headers: map[string]string{
			"Traceparent": "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		},
	})
	handler := Middleware(Config{
		TracerProvider: provider,
		Propagator:     propagation.TraceContext{},
	})(func(core.Invocation) error { return nil })
	if err := handler(invocation); err != nil {
		t.Fatal(err)
	}
	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("exported spans = %d, want 1", len(spans))
	}
	if got := spans[0].Parent.TraceID().String(); got != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("parent trace id = %s", got)
	}
	if !spans[0].Parent.IsRemote() {
		t.Fatal("extracted parent was not marked remote")
	}
}

func TestInvocationAttributesSplitIPv6PeerAddressAndPort(t *testing.T) {
	invocation := core.NewInvocation(core.InvocationOptions{
		Context:   context.Background(),
		Transport: core.TransportTCP,
		Operation: core.MessageOperation(7),
		Remote:    "[2001:db8::1]:9443",
	})
	values := make(map[attribute.Key]attribute.Value)
	for _, item := range invocationAttributes(invocation) {
		values[item.Key] = item.Value
	}
	if got := values["network.peer.address"].AsString(); got != "2001:db8::1" {
		t.Fatalf("network.peer.address = %q, want IPv6 host", got)
	}
	if got := values["network.peer.port"].AsInt64(); got != 9443 {
		t.Fatalf("network.peer.port = %d, want 9443", got)
	}
}
