package server

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/saramand9/bignet/kpro/core"
)

func TestEndpointUseWrapsMessageDispatch(t *testing.T) {
	endpoint := &Endpoint{
		transport: &fakeTransport{name: "fake"},
		router: core.NewRouter(core.RouterConfig{
			SyncQueueSize:         4,
			AsyncWorkers:          1,
			AsyncQueueSize:        4,
			MaxInflightPerSession: 4,
			SessionIdleTimeout:    time.Minute,
		}),
	}
	defer func() {
		if err := endpoint.router.Shutdown(context.Background()); err != nil {
			t.Fatalf("shutdown router: %v", err)
		}
	}()

	callSeen := make(chan struct{}, 1)
	handlerSeen := make(chan struct{}, 1)
	endpoint.Use(func(next core.InvocationHandler) core.InvocationHandler {
		return func(ctx core.Invocation) error {
			if got := ctx.Operation(); got != core.MessageOperation(core.MsgEcho) {
				t.Fatalf("operation = %q", got)
			}
			if payload, ok := ctx.Payload().([]byte); !ok || string(payload) != "hello" {
				t.Fatalf("payload = %T %q", ctx.Payload(), ctx.Payload())
			}
			callSeen <- struct{}{}
			return next(ctx)
		}
	})
	endpoint.HandleSync(core.MsgEcho, func(ctx core.Context) error {
		handlerSeen <- struct{}{}
		return nil
	})

	session := &fakeSession{id: 10, done: make(chan struct{})}
	if err := endpoint.dispatch(core.NewContext(session, core.Message{
		ID:   core.MsgEcho,
		Seq:  1,
		Data: []byte("hello"),
	})); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	select {
	case <-callSeen:
	case <-time.After(time.Second):
		t.Fatal("middleware was not called")
	}
	select {
	case <-handlerSeen:
	case <-time.After(time.Second):
		t.Fatal("handler was not called")
	}
}

func TestServerUseAppliesToFutureMessageEndpoints(t *testing.T) {
	app := New()
	callSeen := make(chan struct{}, 1)
	handlerSeen := make(chan struct{}, 1)
	app.Use(func(next core.InvocationHandler) core.InvocationHandler {
		return func(ctx core.Invocation) error {
			if ctx.Transport() != core.TransportTCP {
				t.Fatalf("transport = %s", ctx.Transport())
			}
			callSeen <- struct{}{}
			return next(ctx)
		}
	})

	endpoint := app.Mount(&fakeTransport{name: string(core.TransportTCP)})
	defer shutdownEndpointRouter(t, endpoint)
	endpoint.HandleSync(core.MsgEcho, func(ctx core.Context) error {
		handlerSeen <- struct{}{}
		return nil
	})

	dispatchTestEcho(t, endpoint)
	waitForSignal(t, callSeen, "middleware")
	waitForSignal(t, handlerSeen, "handler")
}

func TestServerUseAppliesToMountedMessageEndpoints(t *testing.T) {
	app := New()
	endpoint := app.Mount(&fakeTransport{name: string(core.TransportTCP)})
	defer shutdownEndpointRouter(t, endpoint)

	callSeen := make(chan struct{}, 1)
	handlerSeen := make(chan struct{}, 1)
	app.Use(func(next core.InvocationHandler) core.InvocationHandler {
		return func(ctx core.Invocation) error {
			callSeen <- struct{}{}
			return next(ctx)
		}
	})
	endpoint.HandleSync(core.MsgEcho, func(ctx core.Context) error {
		handlerSeen <- struct{}{}
		return nil
	})

	dispatchTestEcho(t, endpoint)
	waitForSignal(t, callSeen, "middleware")
	waitForSignal(t, handlerSeen, "handler")
}

func dispatchTestEcho(t *testing.T, endpoint *Endpoint) {
	t.Helper()
	session := &fakeSession{id: 10, done: make(chan struct{})}
	if err := endpoint.dispatch(core.NewContext(session, core.Message{
		ID:   core.MsgEcho,
		Seq:  1,
		Data: []byte("hello"),
	})); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
}

func waitForSignal(t *testing.T, ch <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatalf("%s was not called", name)
	}
}

func shutdownEndpointRouter(t *testing.T, endpoint *Endpoint) {
	t.Helper()
	if err := endpoint.router.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown router: %v", err)
	}
}

type fakeTransport struct {
	name string
}

func (t *fakeTransport) Name() string {
	return t.name
}

func (t *fakeTransport) ListenAndServe(context.Context, core.Handler) error {
	return nil
}

func (t *fakeTransport) Shutdown(context.Context) error {
	return nil
}

type fakeSession struct {
	id   uint64
	done chan struct{}
}

func (s *fakeSession) ID() uint64 {
	return s.id
}

func (s *fakeSession) Transport() core.Transport {
	return core.TransportTCP
}

func (s *fakeSession) RemoteAddr() net.Addr {
	return nil
}

func (s *fakeSession) Send(core.Message) error {
	return nil
}

func (s *fakeSession) Close() error {
	close(s.done)
	return nil
}

func (s *fakeSession) Done() <-chan struct{} {
	return s.done
}
