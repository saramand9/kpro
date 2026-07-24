package core

import (
	"context"
	"errors"
	"testing"
)

func TestRouterDoesNotExposeHandlerErrorDetails(t *testing.T) {
	message := dispatchMessageError(t, func(Context) error {
		return errors.New("database password=secret")
	})
	if got := string(message.Data); got != "internal error" {
		t.Fatalf("client error = %q, want internal error", got)
	}
}

func TestRouterDoesNotExposeRecoveredPanicDetails(t *testing.T) {
	message := dispatchMessageError(t, func(Context) error {
		panic("api-key=secret")
	})
	if got := string(message.Data); got != "internal error" {
		t.Fatalf("client error = %q, want internal error", got)
	}
}

func TestRouterUsesStablePublicClassifiedError(t *testing.T) {
	message := dispatchMessageError(t, func(Context) error {
		return ErrRateLimited
	})
	if got := string(message.Data); got != "rate limit exceeded" {
		t.Fatalf("client error = %q, want rate limit exceeded", got)
	}
}

func TestRouterDoesNotExposeCloseReasonForDrainedQueue(t *testing.T) {
	router := NewRouter(RouterConfig{SyncQueueSize: 2, MaxInflightPerSession: 3})
	started := make(chan struct{})
	release := make(chan struct{})
	router.HandleSync(10, func(Context) error {
		select {
		case <-started:
		default:
			close(started)
		}
		<-release
		return nil
	})
	session := newFakeSession(9)
	if err := router.Dispatch(NewContext(session, Message{ID: 10, Seq: 1})); err != nil {
		t.Fatalf("dispatch first: %v", err)
	}
	<-started
	if err := router.Dispatch(NewContext(session, Message{ID: 10, Seq: 2})); err != nil {
		t.Fatalf("dispatch queued: %v", err)
	}

	value, ok := router.sessions.Load(sessionExecutorKey{id: session.ID(), done: session.Done()})
	if !ok {
		t.Fatal("session executor not found")
	}
	value.(*sessionExecutor).close()
	message := session.waitForMessage(t)
	if got := string(message.Data); got != "internal error" {
		t.Fatalf("drained queue error = %q, want internal error", got)
	}
	close(release)
	if err := router.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown router: %v", err)
	}
}

func dispatchMessageError(t *testing.T, handler Handler) Message {
	t.Helper()
	router := NewRouter()
	defer func() {
		if err := router.Shutdown(context.Background()); err != nil {
			t.Fatalf("shutdown router: %v", err)
		}
	}()
	router.HandleSync(10, handler)
	session := newFakeSession(1)
	defer func() { _ = session.Close() }()
	if err := router.Dispatch(NewContext(session, Message{ID: 10, Seq: 7})); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	message := session.waitForMessage(t)
	if message.ID != MsgError || message.Seq != 7 {
		t.Fatalf("error response id=%d seq=%d, want id=%d seq=7", message.ID, message.Seq, MsgError)
	}
	return message
}
