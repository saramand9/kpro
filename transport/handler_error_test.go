package transport

import (
	"errors"
	"net"
	"testing"

	"github.com/saramand9/kpro/core"
)

func TestContinueAfterHandlerErrorPreservesErrorAndMetadata(t *testing.T) {
	wantErr := errors.New("handler failed")
	session := &handlerErrorTestSession{id: 7, remote: testAddr("peer:9000")}
	var observed HandlerErrorEvent
	continued := ContinueAfterHandlerError(
		HandlerErrorContinue,
		func(event HandlerErrorEvent) { observed = event },
		session,
		core.Message{ID: 11, Seq: 13},
		wantErr,
	)
	if !continued {
		t.Fatal("ContinueAfterHandlerError() = false, want true")
	}
	if !errors.Is(observed.Err, wantErr) {
		t.Fatalf("observed error = %v, want errors.Is %v", observed.Err, wantErr)
	}
	if observed.Transport != core.TransportTCP || observed.ConnID != 7 || observed.RemoteAddr != "peer:9000" || observed.MessageID != 11 || observed.Sequence != 13 {
		t.Fatalf("observed event = %+v", observed)
	}
}

func TestContinueAfterHandlerErrorIsolatesObserverPanicAndDefaultsClosed(t *testing.T) {
	continued := ContinueAfterHandlerError(
		HandlerErrorCloseSession,
		func(HandlerErrorEvent) { panic("observer panic") },
		nil,
		core.Message{},
		errors.New("handler failed"),
	)
	if continued {
		t.Fatal("ContinueAfterHandlerError() = true, want default close policy")
	}
}

func TestContinueAfterHandlerErrorKeepsSessionForHandledDispatchRejection(t *testing.T) {
	var observed error
	continued := ContinueAfterHandlerError(
		HandlerErrorCloseSession,
		func(event HandlerErrorEvent) { observed = event.Err },
		nil,
		core.Message{},
		core.ErrDispatchQueueFull,
	)
	if !continued {
		t.Fatal("handled Router rejection closed the session")
	}
	if !errors.Is(observed, core.ErrDispatchQueueFull) {
		t.Fatalf("observer error = %v", observed)
	}
}

func TestInvokeHandlerReturnsResultAndRecoversPanics(t *testing.T) {
	wantErr := errors.New("handler failed")
	err := InvokeHandler(func(ctx core.Context) error {
		message := ctx.Message()
		if message.ID != 17 || message.Seq != 19 {
			t.Fatalf("message = %+v", message)
		}
		return wantErr
	}, nil, core.Message{ID: 17, Seq: 19})
	if !errors.Is(err, wantErr) {
		t.Fatalf("InvokeHandler() error = %v, want %v", err, wantErr)
	}

	panicErr := errors.New("handler panic")
	err = InvokeHandler(func(core.Context) error {
		panic(panicErr)
	}, nil, core.Message{})
	if !errors.Is(err, core.ErrRecovered) || !errors.Is(err, panicErr) {
		t.Fatalf("panic error = %v, want ErrRecovered and original panic", err)
	}
	if core.ErrorKindOf(err) != core.ErrorKindRecovered {
		t.Fatalf("panic error kind = %q, want %q", core.ErrorKindOf(err), core.ErrorKindRecovered)
	}

	typedPanic := &handlerPanicTestError{marker: "typed handler panic"}
	err = InvokeHandler(func(core.Context) error {
		panic(typedPanic)
	}, nil, core.Message{})
	var recoveredTyped *handlerPanicTestError
	if !errors.As(err, &recoveredTyped) {
		t.Fatalf("panic error = %v, want errors.As *handlerPanicTestError", err)
	}
	if recoveredTyped != typedPanic {
		t.Fatalf("recovered typed error = %p, want %p", recoveredTyped, typedPanic)
	}

	err = InvokeHandler(func(core.Context) error {
		panic("panic value")
	}, nil, core.Message{})
	if !errors.Is(err, core.ErrRecovered) {
		t.Fatalf("value panic error = %v, want ErrRecovered", err)
	}
	if err := InvokeHandler(nil, nil, core.Message{}); !errors.Is(err, core.ErrNilHandler) {
		t.Fatalf("nil handler error = %v, want %v", err, core.ErrNilHandler)
	}
}

func TestRecoveredDispatchErrorStillUsesHandlerPolicy(t *testing.T) {
	err := InvokeHandler(func(core.Context) error {
		panic(core.ErrDispatchQueueFull)
	}, nil, core.Message{})
	if !errors.Is(err, core.ErrRecovered) || !errors.Is(err, core.ErrDispatchQueueFull) {
		t.Fatalf("panic error = %v", err)
	}
	if ContinueAfterHandlerError(HandlerErrorCloseSession, nil, nil, core.Message{}, err) {
		t.Fatal("recovered panic bypassed default close policy")
	}
	if !ContinueAfterHandlerError(HandlerErrorContinue, nil, nil, core.Message{}, err) {
		t.Fatal("recovered panic ignored explicit continue policy")
	}
}

func TestHandlerErrorPolicyRejectsUnknownValue(t *testing.T) {
	if err := HandlerErrorPolicy(255).Validate(); err == nil {
		t.Fatal("Validate() error = nil, want unknown policy error")
	}
}

type handlerErrorTestSession struct {
	id     uint64
	remote net.Addr
}

func (s *handlerErrorTestSession) ID() uint64              { return s.id }
func (*handlerErrorTestSession) Transport() core.Transport { return core.TransportTCP }
func (s *handlerErrorTestSession) RemoteAddr() net.Addr    { return s.remote }
func (*handlerErrorTestSession) Send(core.Message) error   { return nil }
func (*handlerErrorTestSession) Close() error              { return nil }
func (*handlerErrorTestSession) Done() <-chan struct{}     { return nil }

type testAddr string

func (a testAddr) Network() string { return "test" }
func (a testAddr) String() string  { return string(a) }

type handlerPanicTestError struct {
	marker string
}

func (e *handlerPanicTestError) Error() string {
	return e.marker
}
