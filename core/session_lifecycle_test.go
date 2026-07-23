// File: core/session_lifecycle_test.go
// Purpose: Verifies optional session lifecycle adaptation and message cancellation.

package core

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

func TestSessionDoneUsesOptionalLifecycleCapability(t *testing.T) {
	withLifecycle := newFakeSession(1)
	if got := SessionDone(withLifecycle); got != withLifecycle.Done() {
		t.Fatal("SessionDone did not return the session lifecycle channel")
	}

	withoutLifecycle := &sessionWithoutLifecycle{id: 2}
	if got := SessionDone(withoutLifecycle); got != nil {
		t.Fatalf("SessionDone = %v, want nil", got)
	}
	if got := SessionDone(nil); got != nil {
		t.Fatalf("SessionDone(nil) = %v, want nil", got)
	}
}

func TestSessionContextUsesDoneChannelWithoutBridge(t *testing.T) {
	session := newFakeSession(1)
	ctx := SessionContext(session)

	if ctx.Done() != session.Done() {
		t.Fatal("SessionContext did not directly reuse the session Done channel")
	}
	if err := ctx.Err(); err != nil {
		t.Fatalf("context was canceled before session close: %v", err)
	}

	if err := session.Close(); err != nil {
		t.Fatalf("close session: %v", err)
	}
	waitForClosed(t, ctx.Done(), "session context was not canceled")
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("context error = %v, want context.Canceled", ctx.Err())
	}
}

func TestMessageInvocationContextFollowsSessionLifecycle(t *testing.T) {
	session := newFakeSession(1)
	invocation := NewMessageInvocation(context.Background(), NewContext(session, Message{ID: 10}))

	if invocation.Context().Done() != session.Done() {
		t.Fatal("message invocation created an intermediary waiter for a background parent")
	}
	if err := session.Close(); err != nil {
		t.Fatalf("close session: %v", err)
	}
	waitForClosed(t, invocation.Context().Done(), "message invocation context was not canceled")
}

func TestMessageInvocationMergesCancelableParentAndSession(t *testing.T) {
	t.Run("session closes", func(t *testing.T) {
		parent, cancel := context.WithCancel(context.Background())
		defer cancel()
		session := newFakeSession(1)
		invocation := NewMessageInvocation(parent, NewContext(session, Message{ID: 10}))
		defer invocation.Release()

		if err := session.Close(); err != nil {
			t.Fatalf("close session: %v", err)
		}
		waitForClosed(t, invocation.Context().Done(), "session close did not cancel merged context")
		if !errors.Is(context.Cause(invocation.Context()), ErrClosed) {
			t.Fatalf("context cause = %v, want ErrClosed", context.Cause(invocation.Context()))
		}
	})

	t.Run("parent cancels", func(t *testing.T) {
		parent, cancel := context.WithCancel(context.Background())
		session := newFakeSession(2)
		invocation := NewMessageInvocation(parent, NewContext(session, Message{ID: 10}))
		defer func() {
			invocation.Release()
			_ = session.Close()
		}()

		cancel()
		waitForClosed(t, invocation.Context().Done(), "parent did not cancel merged context")
		if !errors.Is(invocation.Context().Err(), context.Canceled) {
			t.Fatalf("context error = %v, want context.Canceled", invocation.Context().Err())
		}
	})

	t.Run("handler cleanup", func(t *testing.T) {
		parent, cancel := context.WithCancel(context.Background())
		defer cancel()
		session := newFakeSession(3)
		invocation := NewMessageInvocation(parent, NewContext(session, Message{ID: 10}))

		invocation.Release()
		invocation.Release()
		waitForClosed(t, invocation.Context().Done(), "invocation cleanup did not release merged context")
		if err := session.Close(); err != nil {
			t.Fatalf("close session: %v", err)
		}
	})
}

func TestRouterMiddlewareContextCancelsWhenSessionCloses(t *testing.T) {
	router := NewRouter(RouterConfig{
		SyncQueueSize:         4,
		AsyncWorkers:          1,
		AsyncQueueSize:        4,
		MaxInflightPerSession: 4,
		SessionIdleTimeout:    time.Minute,
	})
	defer func() {
		if err := router.Shutdown(context.Background()); err != nil {
			t.Fatalf("shutdown router: %v", err)
		}
	}()

	middlewareStarted := make(chan struct{})
	handlerCalled := make(chan struct{})
	router.Use(func(next InvocationHandler) InvocationHandler {
		return func(invocation Invocation) error {
			close(middlewareStarted)
			<-invocation.Context().Done()
			return next(invocation)
		}
	})
	router.HandleSync(10, func(Context) error {
		close(handlerCalled)
		return nil
	})

	session := newFakeSession(1)
	if err := router.Dispatch(NewContext(session, Message{ID: 10})); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	waitForClosed(t, middlewareStarted, "middleware did not start")
	if err := session.Close(); err != nil {
		t.Fatalf("close session: %v", err)
	}
	waitForClosed(t, handlerCalled, "session close did not unblock middleware context")
}

type sessionWithoutLifecycle struct {
	id uint64
}

func (s *sessionWithoutLifecycle) ID() uint64 {
	return s.id
}

func (*sessionWithoutLifecycle) Transport() Transport {
	return TransportTCP
}

func (*sessionWithoutLifecycle) RemoteAddr() net.Addr {
	return nil
}

func (*sessionWithoutLifecycle) Send(Message) error {
	return nil
}

func (*sessionWithoutLifecycle) Close() error {
	return nil
}
