package otelx

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/saramand9/kpro/core"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func TestMiddlewareSpanReachesMessageHandlerContext(t *testing.T) {
	provider := sdktrace.NewTracerProvider()
	defer func() { _ = provider.Shutdown(context.Background()) }()
	router := core.NewRouter()
	defer func() {
		if err := router.Shutdown(context.Background()); err != nil {
			t.Fatalf("shutdown router: %v", err)
		}
	}()
	router.Use(Middleware(Config{TracerProvider: provider}))

	spanContexts := make(chan trace.SpanContext, 1)
	router.HandleSync(42, func(ctx core.Context) error {
		spanContexts <- trace.SpanFromContext(core.ContextOf(ctx)).SpanContext()
		return nil
	})

	session := newOTelMessageSession(7)
	defer func() { _ = session.Close() }()
	if err := router.Dispatch(core.NewContext(session, core.Message{ID: 42})); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	select {
	case spanContext := <-spanContexts:
		if !spanContext.IsValid() {
			t.Fatal("message handler did not receive the active OpenTelemetry span")
		}
	case <-time.After(time.Second):
		t.Fatal("message handler was not called")
	}
}

type otelMessageSession struct {
	id   uint64
	done chan struct{}
	once sync.Once
}

func newOTelMessageSession(id uint64) *otelMessageSession {
	return &otelMessageSession{id: id, done: make(chan struct{})}
}

func (s *otelMessageSession) ID() uint64              { return s.id }
func (*otelMessageSession) Transport() core.Transport { return core.TransportTCP }
func (*otelMessageSession) RemoteAddr() net.Addr      { return nil }
func (*otelMessageSession) Send(core.Message) error   { return nil }
func (s *otelMessageSession) Done() <-chan struct{}   { return s.done }
func (s *otelMessageSession) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}
