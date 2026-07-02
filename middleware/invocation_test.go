// File: middleware/invocation_test.go
// Purpose: Defines or tests reusable KPro invocation middleware.

package middleware

import (
	"errors"
	"strings"
	"testing"

	"github.com/saramand9/bignet/kpro/core"
	"golang.org/x/time/rate"
)

func TestInvocationRateLimitRejectsOverLimit(t *testing.T) {
	handlerCalls := 0
	handler := RateLimit(RateLimitConfig{
		Rate:  rate.Limit(1),
		Burst: 1,
	})(func(core.Invocation) error {
		handlerCalls++
		return nil
	})

	ctx := core.NewInvocation(core.InvocationOptions{Operation: "test"})
	if err := handler(ctx); err != nil {
		t.Fatalf("first call should pass: %v", err)
	}
	if err := handler(ctx); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("second call should be rate limited, got %v", err)
	}
	if handlerCalls != 1 {
		t.Fatalf("handler calls = %d, want 1", handlerCalls)
	}
}

func TestInvocationConnRateLimitRemovesLimiterWhenDoneCloses(t *testing.T) {
	handler := ConnRateLimit(RateLimitConfig{
		Rate:  rate.Limit(0.001),
		Burst: 1,
	})(func(core.Invocation) error {
		return nil
	})

	done := make(chan struct{})
	ctx := core.NewInvocation(core.InvocationOptions{ConnID: 9, Done: done})
	if err := handler(ctx); err != nil {
		t.Fatalf("first call should pass: %v", err)
	}
	if err := handler(ctx); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("second call should be rate limited, got %v", err)
	}
	close(done)

	reusedDone := make(chan struct{})
	reusedCtx := core.NewInvocation(core.InvocationOptions{ConnID: 9, Done: reusedDone})
	t.Cleanup(func() {
		close(reusedDone)
	})
	waitForRateLimitCleanup(t, func() bool {
		return handler(reusedCtx) == nil
	})
}

func TestInvocationMaxPayloadRejectsOversizedPayload(t *testing.T) {
	handlerCalled := false
	handler := MaxPayload(3)(func(core.Invocation) error {
		handlerCalled = true
		return nil
	})

	err := handler(core.NewInvocation(core.InvocationOptions{Payload: []byte("toolong")}))
	if err == nil || !strings.Contains(err.Error(), "payload too large") {
		t.Fatalf("expected payload error, got %v", err)
	}
	if handlerCalled {
		t.Fatal("handler should not be called")
	}
}

func TestInvocationMaxPayloadUsesLimiter(t *testing.T) {
	limited := &fakePayloadLimiter{size: 7}
	handlerCalled := false
	handler := MaxPayload(3)(func(core.Invocation) error {
		handlerCalled = true
		return nil
	})

	err := handler(core.NewInvocation(core.InvocationOptions{Payload: limited}))
	if err == nil || !strings.Contains(err.Error(), "payload too large") {
		t.Fatalf("expected payload error, got %v", err)
	}
	if !limited.called {
		t.Fatal("limiter was not called")
	}
	if handlerCalled {
		t.Fatal("handler should not be called")
	}
}

type fakePayloadLimiter struct {
	size   int
	called bool
}

func (p *fakePayloadLimiter) LimitPayload(size int) error {
	p.called = true
	if p.size > size {
		return errors.New("payload too large")
	}
	return nil
}

func TestInvocationRecoverConvertsPanicToError(t *testing.T) {
	handler := Recover()(func(core.Invocation) error {
		panic("boom")
	})

	err := handler(core.NewInvocation(core.InvocationOptions{Operation: "panic"}))
	if err == nil || !strings.Contains(err.Error(), "handler panic: boom") {
		t.Fatalf("expected panic error, got %v", err)
	}
}
