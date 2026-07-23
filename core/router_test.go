// File: core/router_test.go
// Purpose: Defines shared KPro primitives used by transports, routers, and middleware.

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

func TestRouterShutdownDrainsQueuedAsyncTasks(t *testing.T) {
	router := NewRouter(RouterConfig{
		SyncQueueSize:         4,
		AsyncWorkers:          1,
		AsyncQueueSize:        4,
		MaxInflightPerSession: 4,
		SessionIdleTimeout:    time.Minute,
		ShutdownTimeout:       time.Second,
	})

	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondHandled := make(chan struct{})
	router.HandleAsync(10, func(ctx Context) error {
		switch ctx.Message().Seq {
		case 1:
			close(firstStarted)
			<-releaseFirst
		case 2:
			close(secondHandled)
		}
		return nil
	})

	session := newFakeSession(1)
	if err := router.Dispatch(NewContext(session, Message{ID: 10, Seq: 1})); err != nil {
		t.Fatalf("dispatch first async: %v", err)
	}
	waitForClosed(t, firstStarted, "first async handler did not start")
	if err := router.Dispatch(NewContext(session, Message{ID: 10, Seq: 2})); err != nil {
		t.Fatalf("dispatch second async: %v", err)
	}

	shutdownDone := make(chan error, 1)
	go func() {
		shutdownDone <- router.Shutdown(context.Background())
	}()

	select {
	case err := <-shutdownDone:
		t.Fatalf("shutdown completed before blocked async handler was released: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(releaseFirst)
	select {
	case <-secondHandled:
	case <-time.After(time.Second):
		t.Fatal("queued async handler did not run during shutdown")
	}
	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatalf("shutdown router: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown did not complete after async queue drained")
	}
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

func TestRouterRemovesSessionExecutorWhenSessionCloses(t *testing.T) {
	router := NewRouter(RouterConfig{
		SyncQueueSize:         4,
		AsyncWorkers:          1,
		AsyncQueueSize:        4,
		MaxInflightPerSession: 4,
		SessionIdleTimeout:    time.Hour,
	})
	defer func() {
		if err := router.Shutdown(context.Background()); err != nil {
			t.Fatalf("shutdown router: %v", err)
		}
	}()

	handled := make(chan struct{})
	router.HandleSync(10, func(Context) error {
		close(handled)
		return nil
	})

	session := newFakeSession(42)
	if err := router.Dispatch(NewContext(session, Message{ID: 10, Seq: 1})); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	select {
	case <-handled:
	case <-time.After(time.Second):
		t.Fatal("handler did not run")
	}
	waitForRouterSessionCount(t, router, 1)

	if err := session.Close(); err != nil {
		t.Fatalf("close session: %v", err)
	}
	waitForRouterSessionCount(t, router, 0)
}

func TestRouterDoesNotReuseExecutorForDifferentSessionWithSameID(t *testing.T) {
	router := NewRouter(RouterConfig{
		SyncQueueSize:         4,
		AsyncWorkers:          1,
		AsyncQueueSize:        4,
		MaxInflightPerSession: 4,
		SessionIdleTimeout:    time.Hour,
	})
	defer func() {
		if err := router.Shutdown(context.Background()); err != nil {
			t.Fatalf("shutdown router: %v", err)
		}
	}()

	handled := make(chan Session, 2)
	router.HandleSync(10, func(ctx Context) error {
		handled <- ctx.Session()
		return nil
	})
	first := newFakeSession(42)
	second := newFakeSession(42)
	if err := router.Dispatch(NewContext(first, Message{ID: 10, Seq: 1})); err != nil {
		t.Fatalf("dispatch first session: %v", err)
	}
	if got := <-handled; got != first {
		t.Fatalf("first handled session = %p, want %p", got, first)
	}
	if err := router.Dispatch(NewContext(second, Message{ID: 10, Seq: 2})); err != nil {
		t.Fatalf("dispatch replacement session: %v", err)
	}
	if got := <-handled; got != second {
		t.Fatalf("replacement handled session = %p, want %p", got, second)
	}

	value, ok := router.sessions.Load(sessionExecutorKey{id: 42, done: second.Done()})
	if !ok || value.(*sessionExecutor).done != second.Done() {
		t.Fatal("router executor retained the first session lifecycle")
	}
	if got := routerSessionCount(router); got != 2 {
		t.Fatalf("executor count = %d, want one per live session", got)
	}
}

func TestRouterDoesNotStartAsyncWorkersAfterClose(t *testing.T) {
	router := NewRouter(RouterConfig{AsyncWorkers: 1})
	router.Close()
	router.HandleAsync(10, func(Context) error { return nil })

	router.mu.RLock()
	registered := router.routes[10].handler != nil
	router.mu.RUnlock()
	if registered {
		t.Fatal("async route was registered after router close")
	}
	if err := router.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown router: %v", err)
	}
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

func waitForRouterSessionCount(t *testing.T, router *Router, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		got := routerSessionCount(router)
		if got == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	got := routerSessionCount(router)
	t.Fatalf("router session count = %d, want %d", got, want)
}

func routerSessionCount(router *Router) int {
	count := 0
	router.sessions.Range(func(_, _ any) bool {
		count++
		return true
	})
	return count
}

func waitForClosed(t *testing.T, ch <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal(message)
	}
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
	router.Use(func(next InvocationHandler) InvocationHandler {
		return func(inv Invocation) error {
			inv.Set("middleware", "applied")
			return next(inv)
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

func TestRouterMiddlewareWrapsActualHandlerExecution(t *testing.T) {
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

	beforeNext := make(chan struct{})
	afterNext := make(chan struct{})
	handlerRelease := make(chan struct{})
	router.Use(func(next InvocationHandler) InvocationHandler {
		return func(inv Invocation) error {
			close(beforeNext)
			err := next(inv)
			close(afterNext)
			return err
		}
	})
	router.HandleSync(10, func(Context) error {
		<-handlerRelease
		return nil
	})

	if err := router.Dispatch(NewContext(newFakeSession(1), Message{ID: 10, Seq: 1})); err != nil {
		t.Fatalf("dispatch sync: %v", err)
	}
	select {
	case <-beforeNext:
	case <-time.After(time.Second):
		t.Fatal("middleware did not enter before handler")
	}
	select {
	case <-afterNext:
		t.Fatal("middleware completed before handler returned")
	case <-time.After(50 * time.Millisecond):
	}
	close(handlerRelease)
	select {
	case <-afterNext:
	case <-time.After(time.Second):
		t.Fatal("middleware did not complete after handler returned")
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
