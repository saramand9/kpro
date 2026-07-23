// File: core/router_optimization_test.go
// Purpose: Verifies compiled middleware chains and benchmarks Router dispatch.

package core

import (
	"context"
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
