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
