// File: core/router_optimization_test.go
// Purpose: Verifies compiled middleware chains and benchmarks Router dispatch.

package core

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRouterCompilesMiddlewareWhenConfigurationChanges(t *testing.T) {
	router := NewRouter(RouterConfig{
		SyncQueueSize:         8,
		AsyncWorkers:          1,
		AsyncQueueSize:        8,
		MaxInflightPerSession: 8,
		SessionIdleTimeout:    time.Minute,
	})
	defer func() {
		if err := router.Shutdown(context.Background()); err != nil {
			t.Fatalf("shutdown router: %v", err)
		}
	}()

	var builds atomic.Int32
	router.Use(func(next InvocationHandler) InvocationHandler {
		builds.Add(1)
		return func(invocation Invocation) error {
			return next(invocation)
		}
	})

	handled := make(chan struct{}, 4)
	router.HandleSync(10, func(Context) error {
		handled <- struct{}{}
		return nil
	})
	if got := builds.Load(); got != 1 {
		t.Fatalf("middleware builds after registration = %d, want 1", got)
	}

	session := newFakeSession(1)
	for i := 0; i < 4; i++ {
		if err := router.Dispatch(NewContext(session, Message{ID: 10, Seq: uint64(i)})); err != nil {
			t.Fatalf("dispatch %d: %v", i, err)
		}
		waitForClosed(t, handled, "handler did not run")
	}
	if got := builds.Load(); got != 1 {
		t.Fatalf("middleware rebuilt on dispatch: builds=%d", got)
	}
}

func TestRouterCachesMessageOperationAndIsolatesInvocationMetadata(t *testing.T) {
	router := NewRouter(RouterConfig{
		SyncQueueSize:         1,
		AsyncWorkers:          1,
		AsyncQueueSize:        1,
		MaxInflightPerSession: 1,
		SessionIdleTimeout:    time.Minute,
	})
	defer func() {
		if err := router.Shutdown(context.Background()); err != nil {
			t.Fatalf("shutdown router: %v", err)
		}
	}()

	type handlerObservation struct {
		header  string
		attempt any
	}
	observedInvocation := make(chan string, 1)
	observedHandler := make(chan handlerObservation, 1)

	router.HandleSync(42, func(ctx Context) error {
		message := ctx.Message()
		observedHandler <- handlerObservation{
			header:  message.Headers["trace"],
			attempt: message.Meta["attempt"],
		}
		return nil
	})

	// Adding middleware after route registration rebuilds the compiled handler.
	// The route's precomputed operation must remain attached to the route.
	router.Use(func(next InvocationHandler) InvocationHandler {
		return func(invocation Invocation) error {
			observedInvocation <- invocation.Operation()
			invocation.Headers()["trace"] = "middleware"
			invocation.Meta()["attempt"] = 2
			return next(invocation)
		}
	})

	session := newFakeSession(1)
	if err := router.Dispatch(NewContext(session, Message{
		ID:      42,
		Headers: map[string]string{"trace": "original"},
		Meta:    map[string]any{"attempt": 1},
	})); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	if got := <-observedInvocation; got != "message/42" {
		t.Fatalf("operation = %q, want message/42", got)
	}
	got := <-observedHandler
	if got.header != "original" {
		t.Fatalf("handler header = %q, want original", got.header)
	}
	if got.attempt != 1 {
		t.Fatalf("handler attempt = %v, want 1", got.attempt)
	}
}

func TestRouterReusesSessionInvocationContext(t *testing.T) {
	router := NewRouter(RouterConfig{
		SyncQueueSize:         2,
		MaxInflightPerSession: 2,
		SessionIdleTimeout:    time.Minute,
	})
	defer func() {
		if err := router.Shutdown(context.Background()); err != nil {
			t.Fatalf("shutdown router: %v", err)
		}
	}()

	contexts := make(chan context.Context, 2)
	router.Use(func(next InvocationHandler) InvocationHandler {
		return func(invocation Invocation) error {
			contexts <- invocation.Context()
			return next(invocation)
		}
	})
	router.HandleSync(42, func(Context) error { return nil })

	session := newFakeSession(1)
	for sequence := uint64(1); sequence <= 2; sequence++ {
		if err := router.Dispatch(NewContext(session, Message{ID: 42, Seq: sequence})); err != nil {
			t.Fatalf("dispatch %d: %v", sequence, err)
		}
	}
	first := <-contexts
	second := <-contexts
	if first != second {
		t.Fatal("message invocations for one session did not reuse their lifecycle context")
	}
	if first.Done() != session.Done() {
		t.Fatal("cached invocation context did not preserve the session lifetime")
	}
}

func TestAtomicCapacityPropagatesCoalescedWakeups(t *testing.T) {
	const limit = 4
	capacity := newAtomicCapacity(limit)
	for range limit {
		if !capacity.tryAcquire() {
			t.Fatal("failed to fill atomic capacity")
		}
	}

	registry := newWaiterRegistry(limit, 1)
	hold := make(chan struct{})
	results := make(chan error, limit)
	var workers sync.WaitGroup
	for i := range limit {
		workers.Add(1)
		go func(id int) {
			defer workers.Done()
			waiter := newDispatchWaiter(
				registry,
				sessionExecutorKey{id: uint64(id + 1)},
			)
			defer waiter.release()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, err := acquireAtomicCapacity(
				ctx,
				capacity,
				BackpressureWait,
				ErrGlobalInflightLimit,
				nil,
				nil,
				nil,
				nil,
				&waiter,
			)
			results <- err
			if err == nil {
				<-hold
				capacity.release()
			}
		}(i)
	}

	// Several releases can collapse into one buffered notification. Each
	// successful waiter must pass that notification on while slots remain.
	for range limit {
		capacity.release()
	}
	for range limit {
		if err := <-results; err != nil {
			t.Fatalf("waiting acquire failed: %v", err)
		}
	}
	close(hold)
	workers.Wait()
	if got := capacity.used.Load(); got != 0 {
		t.Fatalf("used capacity after releases = %d, want 0", got)
	}
}

func TestSessionReservationPreventsIdleExecutorClose(t *testing.T) {
	router := NewRouter(RouterConfig{
		SyncQueueSize:         1,
		AsyncWorkers:          1,
		AsyncQueueSize:        1,
		MaxInflightPerSession: 1,
		SessionIdleTimeout:    5 * time.Millisecond,
	})
	session := newFakeSession(1)
	executor, err := router.sessionFor(session)
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(20 * time.Millisecond)
	if executor.isClosed() {
		t.Fatal("reserved executor was closed by idle cleanup")
	}
	executor.release()
	waitForRouterSessionCount(t, router, 0)
	if err := router.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func BenchmarkRouterDispatchSync(b *testing.B) {
	for _, middlewareCount := range []int{0, 1, 4} {
		b.Run(middlewareCountName(middlewareCount), func(b *testing.B) {
			router := NewRouter(RouterConfig{
				SyncQueueSize:         1,
				AsyncWorkers:          1,
				AsyncQueueSize:        1,
				MaxInflightPerSession: 2,
				SessionIdleTimeout:    time.Minute,
			})
			for i := 0; i < middlewareCount; i++ {
				router.Use(func(next InvocationHandler) InvocationHandler {
					return func(invocation Invocation) error {
						return next(invocation)
					}
				})
			}
			handled := make(chan struct{})
			router.HandleSync(10, func(Context) error {
				handled <- struct{}{}
				return nil
			})
			session := newFakeSession(1)
			message := Message{ID: 10}

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := router.Dispatch(AcquireContext(session, message)); err != nil {
					b.Fatal(err)
				}
				<-handled
			}
			b.StopTimer()
			if err := router.Shutdown(context.Background()); err != nil {
				b.Fatal(err)
			}
		})
	}
}

func middlewareCountName(count int) string {
	switch count {
	case 0:
		return "no_middleware"
	case 1:
		return "one_middleware"
	default:
		return "four_middlewares"
	}
}
