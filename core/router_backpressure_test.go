// File: core/router_backpressure_test.go
// Purpose: Verifies Router lifecycle, bounded admission, and wait cancellation.

package core

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLifecycleStateString(t *testing.T) {
	tests := []struct {
		state LifecycleState
		want  string
	}{
		{StateCreated, "created"},
		{StateStarting, "starting"},
		{StateRunning, "running"},
		{StateDraining, "draining"},
		{StateStopped, "stopped"},
		{LifecycleState(99), "lifecycle(99)"},
	}
	for _, tt := range tests {
		if got := tt.state.String(); got != tt.want {
			t.Errorf("LifecycleState(%d).String() = %q, want %q", tt.state, got, tt.want)
		}
	}
}

func TestRouterConfigValidateIsStrictButNewRouterRemainsCompatible(t *testing.T) {
	if err := (RouterConfig{}).Validate(); err != nil {
		t.Fatalf("zero RouterConfig validation error = %v", err)
	}
	invalid := RouterConfig{
		SyncQueueSize:      -1,
		AsyncWorkers:       -1,
		AsyncQueueSize:     -1,
		MaxInflight:        -1,
		SessionIdleTimeout: -time.Second,
		Backpressure:       BackpressurePolicy(99),
	}
	if err := invalid.Validate(); err == nil {
		t.Fatal("invalid RouterConfig passed validation")
	}

	router := NewRouter(invalid)
	if router.config.SyncQueueSize != DefaultSyncQueueSize ||
		router.config.MaxInflight != DefaultMaxInflight ||
		router.config.Backpressure != BackpressureReject {
		t.Fatalf("NewRouter() did not preserve compatibility normalization: %+v", router.config)
	}
	shutdownAdmissionRouter(t, router)
}

func TestRouterLifecycle(t *testing.T) {
	router := NewRouter(testRouterConfig(BackpressureReject))
	if got := router.State(); got != StateCreated {
		t.Fatalf("initial state = %s, want %s", got, StateCreated)
	}
	if got := router.Snapshot().State; got != StateCreated {
		t.Fatalf("initial snapshot state = %s, want %s", got, StateCreated)
	}

	handled := make(chan struct{})
	router.HandleSync(1, func(Context) error {
		close(handled)
		return nil
	})
	session := newAdmissionSession(1)
	if err := router.Dispatch(NewContext(session, Message{ID: 1})); err != nil {
		t.Fatalf("Dispatch() error = %v", err)
	}
	waitClosedForAdmissionTest(t, handled, "handler did not run")
	if got := router.State(); got != StateRunning {
		t.Fatalf("state after dispatch = %s, want %s", got, StateRunning)
	}

	if err := router.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if got := router.State(); got != StateStopped {
		t.Fatalf("state after shutdown = %s, want %s", got, StateStopped)
	}
	if err := router.Dispatch(NewContext(session, Message{ID: 1})); !errors.Is(err, ErrRouterClosed) {
		t.Fatalf("Dispatch() after shutdown = %v, want %v", err, ErrRouterClosed)
	}
}

func TestRouterCloseBeforeDispatchStopsLifecycle(t *testing.T) {
	router := NewRouter(testRouterConfig(BackpressureReject))
	router.Close()
	if err := router.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if got := router.State(); got != StateStopped {
		t.Fatalf("state = %s, want %s", got, StateStopped)
	}
}

func TestRouterRejectsAtGlobalInflightLimit(t *testing.T) {
	config := testRouterConfig(BackpressureReject)
	config.MaxInflight = 1
	router := NewRouter(config)
	defer shutdownAdmissionRouter(t, router)

	started := make(chan struct{})
	release := make(chan struct{})
	router.HandleSync(1, func(Context) error {
		close(started)
		<-release
		return nil
	})

	first := newAdmissionSession(1)
	if err := router.Dispatch(NewContext(first, Message{ID: 1, Seq: 1})); err != nil {
		t.Fatalf("first Dispatch() error = %v", err)
	}
	waitClosedForAdmissionTest(t, started, "first handler did not start")

	second := newAdmissionSession(2)
	if err := router.Dispatch(NewContext(second, Message{ID: 1, Seq: 2})); !errors.Is(err, ErrGlobalInflightLimit) {
		t.Fatalf("second Dispatch() error = %v, want %v", err, ErrGlobalInflightLimit)
	}
	stats := router.Snapshot()
	if stats.Inflight != 1 || stats.Accepted != 1 || stats.Rejected != 1 {
		t.Fatalf("stats while full = %+v", stats)
	}

	close(release)
	waitForRouterCompleted(t, router, 1)
}

func TestRouterWaitsForGlobalCapacity(t *testing.T) {
	config := testRouterConfig(BackpressureWait)
	config.MaxInflight = 1
	router := NewRouter(config)
	defer shutdownAdmissionRouter(t, router)

	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var handled atomic.Int32
	router.HandleSync(1, func(ctx Context) error {
		if ctx.Message().Seq == 1 {
			close(firstStarted)
			<-releaseFirst
		}
		handled.Add(1)
		return nil
	})

	if err := router.Dispatch(NewContext(newAdmissionSession(1), Message{ID: 1, Seq: 1})); err != nil {
		t.Fatalf("first Dispatch() error = %v", err)
	}
	waitClosedForAdmissionTest(t, firstStarted, "first handler did not start")

	result := make(chan error, 1)
	go func() {
		result <- router.DispatchContext(
			context.Background(),
			NewContext(newAdmissionSession(2), Message{ID: 1, Seq: 2}),
		)
	}()
	select {
	case err := <-result:
		t.Fatalf("waiting DispatchContext() returned early: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	close(releaseFirst)
	if err := waitAdmissionResult(t, result); err != nil {
		t.Fatalf("waiting DispatchContext() error = %v", err)
	}
	waitForRouterCompleted(t, router, 2)
	if got := handled.Load(); got != 2 {
		t.Fatalf("handled = %d, want 2", got)
	}
	stats := router.Snapshot()
	if stats.Waited != 1 || stats.Accepted != 2 || stats.Rejected != 0 {
		t.Fatalf("stats after wait = %+v", stats)
	}
}

func TestRouterGlobalCapacityWaitHonorsContext(t *testing.T) {
	config := testRouterConfig(BackpressureWait)
	config.MaxInflight = 1
	router := NewRouter(config)
	defer shutdownAdmissionRouter(t, router)

	started := make(chan struct{})
	release := make(chan struct{})
	router.HandleSync(1, func(Context) error {
		close(started)
		<-release
		return nil
	})
	if err := router.Dispatch(NewContext(newAdmissionSession(1), Message{ID: 1, Seq: 1})); err != nil {
		t.Fatalf("first Dispatch() error = %v", err)
	}
	waitClosedForAdmissionTest(t, started, "first handler did not start")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := router.DispatchContext(
		ctx,
		NewContext(newAdmissionSession(2), Message{ID: 1, Seq: 2}),
	)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting DispatchContext() error = %v, want deadline exceeded", err)
	}
	stats := router.Snapshot()
	if stats.Inflight != 1 || stats.Waited != 1 || stats.Rejected != 1 {
		t.Fatalf("stats after canceled wait = %+v", stats)
	}

	close(release)
	waitForRouterCompleted(t, router, 1)
	if got := router.Snapshot().Inflight; got != 0 {
		t.Fatalf("inflight after completion = %d, want 0", got)
	}
}

func TestRouterPerSessionWaitHonorsContext(t *testing.T) {
	config := testRouterConfig(BackpressureWait)
	config.MaxInflight = 4
	config.MaxInflightPerSession = 1
	router := NewRouter(config)
	defer shutdownAdmissionRouter(t, router)

	started := make(chan struct{})
	release := make(chan struct{})
	router.HandleSync(1, func(Context) error {
		close(started)
		<-release
		return nil
	})
	session := newAdmissionSession(1)
	if err := router.Dispatch(NewContext(session, Message{ID: 1, Seq: 1})); err != nil {
		t.Fatalf("first Dispatch() error = %v", err)
	}
	waitClosedForAdmissionTest(t, started, "first handler did not start")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := router.DispatchContext(ctx, NewContext(session, Message{ID: 1, Seq: 2}))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second DispatchContext() error = %v, want deadline exceeded", err)
	}
	if got := router.Snapshot().Inflight; got != 1 {
		t.Fatalf("inflight after canceled per-session wait = %d, want 1", got)
	}

	close(release)
	waitForRouterCompleted(t, router, 1)
}

func TestRouterSyncQueueWaitHonorsContextWithoutLeakingAdmission(t *testing.T) {
	config := testRouterConfig(BackpressureWait)
	config.SyncQueueSize = 1
	config.MaxInflight = 4
	config.MaxInflightPerSession = 4
	router := NewRouter(config)
	defer shutdownAdmissionRouter(t, router)

	started := make(chan struct{})
	release := make(chan struct{})
	router.HandleSync(1, func(ctx Context) error {
		if ctx.Message().Seq == 1 {
			close(started)
			<-release
		}
		return nil
	})
	session := newAdmissionSession(1)
	if err := router.Dispatch(NewContext(session, Message{ID: 1, Seq: 1})); err != nil {
		t.Fatalf("first Dispatch() error = %v", err)
	}
	waitClosedForAdmissionTest(t, started, "first handler did not start")
	if err := router.Dispatch(NewContext(session, Message{ID: 1, Seq: 2})); err != nil {
		t.Fatalf("queued Dispatch() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := router.DispatchContext(ctx, NewContext(session, Message{ID: 1, Seq: 3}))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("third DispatchContext() error = %v, want deadline exceeded", err)
	}
	stats := router.Snapshot()
	if stats.Inflight != 2 || stats.SyncQueueDepth != 1 {
		t.Fatalf("stats after queue wait cancellation = %+v", stats)
	}

	close(release)
	waitForRouterCompleted(t, router, 2)
}

func TestRouterRejectsWhenAsyncQueueIsFull(t *testing.T) {
	config := testRouterConfig(BackpressureReject)
	config.AsyncWorkers = 1
	config.AsyncQueueSize = 1
	config.MaxInflight = 4
	config.MaxInflightPerSession = 4
	router := NewRouter(config)
	defer shutdownAdmissionRouter(t, router)

	started := make(chan struct{})
	release := make(chan struct{})
	router.HandleAsync(1, func(ctx Context) error {
		if ctx.Message().Seq == 1 {
			close(started)
			<-release
		}
		return nil
	})
	session := newAdmissionSession(1)
	if err := router.Dispatch(NewContext(session, Message{ID: 1, Seq: 1})); err != nil {
		t.Fatalf("first Dispatch() error = %v", err)
	}
	waitClosedForAdmissionTest(t, started, "first async handler did not start")
	if err := router.Dispatch(NewContext(session, Message{ID: 1, Seq: 2})); err != nil {
		t.Fatalf("queued Dispatch() error = %v", err)
	}
	if err := router.Dispatch(NewContext(session, Message{ID: 1, Seq: 3})); !errors.Is(err, ErrDispatchQueueFull) {
		t.Fatalf("third Dispatch() error = %v, want %v", err, ErrDispatchQueueFull)
	}
	stats := router.Snapshot()
	if stats.Inflight != 2 || stats.AsyncQueueDepth != 1 || stats.Rejected != 1 {
		t.Fatalf("stats while async queue full = %+v", stats)
	}

	close(release)
	waitForRouterCompleted(t, router, 2)
}

func TestRouterConcurrentDispatchAndClose(t *testing.T) {
	config := testRouterConfig(BackpressureReject)
	config.SyncQueueSize = 8
	config.MaxInflight = 16
	config.MaxInflightPerSession = 8
	router := NewRouter(config)
	router.HandleSync(1, func(Context) error { return nil })

	session := newAdmissionSession(1)
	var dispatchers sync.WaitGroup
	for i := 0; i < 32; i++ {
		dispatchers.Add(1)
		go func(seq uint64) {
			defer dispatchers.Done()
			_ = router.Dispatch(NewContext(session, Message{ID: 1, Seq: seq}))
		}(uint64(i))
	}
	router.Close()
	dispatchers.Wait()
	if err := router.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	stats := router.Snapshot()
	if stats.Inflight != 0 || stats.State != StateStopped {
		t.Fatalf("final stats = %+v", stats)
	}
}

func TestRouterStartupCannotReviveDrainingState(t *testing.T) {
	router := NewRouter(testRouterConfig(BackpressureReject))
	defer shutdownAdmissionRouter(t, router)

	router.state.Store(uint32(StateStarting))
	router.beginDraining()
	if err := router.publishRunning(); !errors.Is(err, ErrRouterClosed) {
		t.Fatalf("publishRunning() error = %v, want %v", err, ErrRouterClosed)
	}
	if got := router.State(); got != StateDraining {
		t.Fatalf("state = %s, want %s", got, StateDraining)
	}
}

func testRouterConfig(policy BackpressurePolicy) RouterConfig {
	return RouterConfig{
		SyncQueueSize:         4,
		AsyncWorkers:          1,
		AsyncQueueSize:        4,
		MaxInflightPerSession: 4,
		MaxInflight:           8,
		Backpressure:          policy,
		SessionIdleTimeout:    time.Hour,
		ShutdownTimeout:       time.Second,
	}
}

func shutdownAdmissionRouter(t *testing.T, router *Router) {
	t.Helper()
	if err := router.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

func waitForRouterCompleted(t *testing.T, router *Router, want uint64) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if router.Snapshot().Completed == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("completed = %d, want %d", router.Snapshot().Completed, want)
}

func waitClosedForAdmissionTest(t *testing.T, done <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal(message)
	}
}

func waitAdmissionResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for DispatchContext()")
		return nil
	}
}

type admissionSession struct {
	id   uint64
	done chan struct{}
	once sync.Once
}

func newAdmissionSession(id uint64) *admissionSession {
	return &admissionSession{id: id, done: make(chan struct{})}
}

func (s *admissionSession) ID() uint64            { return s.id }
func (s *admissionSession) Transport() Transport  { return TransportTCP }
func (s *admissionSession) RemoteAddr() net.Addr  { return nil }
func (s *admissionSession) Send(Message) error    { return nil }
func (s *admissionSession) Done() <-chan struct{} { return s.done }
func (s *admissionSession) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}
