// File: core/router_reentrant_test.go
// Purpose: Verifies middleware constructors are never called under Router locks.

package core

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestRouterMiddlewareConstructorCanReenterRegistration(t *testing.T) {
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

	firstHandled := make(chan struct{}, 1)
	reentrantHandled := make(chan struct{}, 1)
	router.HandleSync(10, func(Context) error {
		firstHandled <- struct{}{}
		return nil
	})

	var reentered atomic.Bool
	middleware := func(next InvocationHandler) InvocationHandler {
		if reentered.CompareAndSwap(false, true) {
			router.HandleSync(11, func(Context) error {
				reentrantHandled <- struct{}{}
				return nil
			})
		}
		return func(invocation Invocation) error {
			return next(invocation)
		}
	}

	useDone := make(chan struct{})
	go func() {
		router.Use(middleware)
		close(useDone)
	}()
	waitForClosed(t, useDone, "middleware constructor deadlocked while reentering Router")

	session := newFakeSession(1)
	if err := router.Dispatch(NewContext(session, Message{ID: 10})); err != nil {
		t.Fatalf("dispatch first route: %v", err)
	}
	waitForClosed(t, firstHandled, "first route was not handled")
	if err := router.Dispatch(NewContext(session, Message{ID: 11})); err != nil {
		t.Fatalf("dispatch reentrant route: %v", err)
	}
	waitForClosed(t, reentrantHandled, "reentrant route was not handled")
}
