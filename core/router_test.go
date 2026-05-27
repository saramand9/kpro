package core

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"
)

func TestRouterSyncHandlersKeepSessionOrder(t *testing.T) {
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

	firstStarted := make(chan struct{})
	firstRelease := make(chan struct{})
	secondStarted := make(chan struct{})

	router.HandleSync(10, func(Context) error {
		close(firstStarted)
		<-firstRelease
		return nil
	})
	router.HandleSync(11, func(Context) error {
		close(secondStarted)
		return nil
	})

	session := newFakeSession(1)
	if err := router.Dispatch(NewContext(session, Message{ID: 10, Seq: 1})); err != nil {
		t.Fatalf("dispatch first: %v", err)
	}
	<-firstStarted
	if err := router.Dispatch(NewContext(session, Message{ID: 11, Seq: 2})); err != nil {
		t.Fatalf("dispatch second: %v", err)
	}

	select {
	case <-secondStarted:
		t.Fatal("second sync handler started before first completed")
	case <-time.After(50 * time.Millisecond):
	}

	close(firstRelease)
	select {
	case <-secondStarted:
	case <-time.After(time.Second):
		t.Fatal("second sync handler did not start after first completed")
	}
}

func TestRouterAsyncHandlerDoesNotWaitForBlockedSyncHandler(t *testing.T) {
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

	syncStarted := make(chan struct{})
	syncRelease := make(chan struct{})
	asyncDone := make(chan struct{})

	router.HandleSync(10, func(Context) error {
		close(syncStarted)
		<-syncRelease
		return nil
	})
	router.HandleAsync(11, func(Context) error {
		close(asyncDone)
		return nil
	})

	session := newFakeSession(1)
	if err := router.Dispatch(NewContext(session, Message{ID: 10, Seq: 1})); err != nil {
		t.Fatalf("dispatch sync: %v", err)
	}
	<-syncStarted
	if err := router.Dispatch(NewContext(session, Message{ID: 11, Seq: 2})); err != nil {
		t.Fatalf("dispatch async: %v", err)
	}

	select {
	case <-asyncDone:
	case <-time.After(time.Second):
		t.Fatal("async handler waited for blocked sync handler")
	}
	close(syncRelease)
}

func TestRouterRejectsWhenSessionInflightLimitIsReached(t *testing.T) {
	router := NewRouter(RouterConfig{
		SyncQueueSize:         4,
		AsyncWorkers:          1,
		AsyncQueueSize:        4,
		MaxInflightPerSession: 1,
		SessionIdleTimeout:    time.Minute,
	})
	defer func() {
		if err := router.Shutdown(context.Background()); err != nil {
			t.Fatalf("shutdown router: %v", err)
		}
	}()

	started := make(chan struct{})
	release := make(chan struct{})
	router.HandleSync(10, func(Context) error {
		close(started)
		<-release
		return nil
	})

	session := newFakeSession(1)
	if err := router.Dispatch(NewContext(session, Message{ID: 10, Seq: 1})); err != nil {
		t.Fatalf("dispatch first: %v", err)
	}
	<-started

	err := router.Dispatch(NewContext(session, Message{ID: 10, Seq: 2}))
	if err != ErrTooManyInflight {
		t.Fatalf("expected ErrTooManyInflight, got %v", err)
	}

	msg := session.waitForMessage(t)
	if msg.ID != MsgError || msg.Seq != 2 {
		t.Fatalf("unexpected error message: id=%d seq=%d", msg.ID, msg.Seq)
	}
	close(release)
}

type fakeSession struct {
	id   uint64
	done chan struct{}
	sent chan Message
	once sync.Once
}

func newFakeSession(id uint64) *fakeSession {
	return &fakeSession{
		id:   id,
		done: make(chan struct{}),
		sent: make(chan Message, 8),
	}
}

func (s *fakeSession) ID() uint64 {
	return s.id
}

func (s *fakeSession) Transport() Transport {
	return TransportTCP
}

func (s *fakeSession) RemoteAddr() net.Addr {
	return nil
}

func (s *fakeSession) Send(msg Message) error {
	select {
	case s.sent <- msg:
		return nil
	case <-s.done:
		return ErrClosed
	}
}

func (s *fakeSession) Close() error {
	s.once.Do(func() {
		close(s.done)
	})
	return nil
}

func (s *fakeSession) Done() <-chan struct{} {
	return s.done
}

func (s *fakeSession) waitForMessage(t *testing.T) Message {
	t.Helper()
	select {
	case msg := <-s.sent:
		return msg
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for session message")
	}
	return Message{}
}

func TestBaseContextValuesAreLazy(t *testing.T) {
	ctx := NewContext(newFakeSession(1), Message{ID: 10})
	if ctx.values != nil {
		t.Fatal("new context allocated values map before Set")
	}
	if value, ok := ctx.Get("missing"); ok || value != nil {
		t.Fatalf("unexpected missing value: value=%v ok=%v", value, ok)
	}
	ctx.Set("key", "value")
	if ctx.values == nil {
		t.Fatal("Set did not allocate values map")
	}
	value, ok := ctx.Get("key")
	if !ok || value != "value" {
		t.Fatalf("unexpected stored value: value=%v ok=%v", value, ok)
	}
}

func TestRouterUseAfterHandleRebuildsMiddlewareChain(t *testing.T) {
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

	called := make(chan struct{}, 1)
	router.HandleSync(10, func(ctx Context) error {
		value, ok := ctx.Get("middleware")
		if !ok || value != "applied" {
			t.Fatalf("middleware value not applied: value=%v ok=%v", value, ok)
		}
		called <- struct{}{}
		return nil
	})
	router.Use(func(next Handler) Handler {
		return func(ctx Context) error {
			ctx.Set("middleware", "applied")
			return next(ctx)
		}
	})

	if err := router.Dispatch(NewContext(newFakeSession(1), Message{ID: 10, Seq: 1})); err != nil {
		t.Fatalf("dispatch sync: %v", err)
	}
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("handler was not called")
	}
}

func TestAcquireContextReturnsFreshContext(t *testing.T) {
	ctx := AcquireContext(newFakeSession(1), Message{ID: 10, Seq: 1})
	ctx.Set("key", "value")

	fresh := AcquireContext(newFakeSession(2), Message{ID: 11, Seq: 2})
	if fresh == ctx {
		t.Fatal("AcquireContext reused context object")
	}
	if fresh.ConnID() != 2 || fresh.Message().ID != 11 || fresh.Message().Seq != 2 {
		t.Fatalf("fresh context was not initialized: conn=%d msg=%+v", fresh.ConnID(), fresh.Message())
	}
	if value, ok := fresh.Get("key"); ok || value != nil {
		t.Fatalf("fresh context leaked value: value=%v ok=%v", value, ok)
	}
}

func TestCloneContextIsolatesMutableMessageData(t *testing.T) {
	session := newFakeSession(1)
	ctx := AcquireContext(session, Message{
		ID:      10,
		Seq:     1,
		Data:    []byte("payload"),
		Headers: map[string]string{"trace": "abc"},
		Meta:    map[string]interface{}{"attempt": 1},
	})
	ctx.Set("middleware", "applied")

	cloned := CloneContext(ctx)

	msg := ctx.Message()
	msg.Data[0] = 'X'
	msg.Headers["trace"] = "mutated"
	msg.Meta["attempt"] = 2

	if cloned.Session() != session {
		t.Fatal("cloned context did not keep session")
	}
	clonedMsg := cloned.Message()
	if clonedMsg.ID != 10 || clonedMsg.Seq != 1 || string(clonedMsg.Data) != "payload" {
		t.Fatalf("unexpected cloned message: id=%d seq=%d data=%q", clonedMsg.ID, clonedMsg.Seq, string(clonedMsg.Data))
	}
	if clonedMsg.Headers["trace"] != "abc" {
		t.Fatalf("cloned headers were not isolated: %q", clonedMsg.Headers["trace"])
	}
	if clonedMsg.Meta["attempt"] != 1 {
		t.Fatalf("cloned meta was not isolated: %v", clonedMsg.Meta["attempt"])
	}
	if value, ok := cloned.Get("middleware"); !ok || value != "applied" {
		t.Fatalf("cloned values missing: value=%v ok=%v", value, ok)
	}
}
