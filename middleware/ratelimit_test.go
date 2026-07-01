package middleware

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/saramand9/bignet/kpro/core"
	"golang.org/x/time/rate"
)

func TestRateLimitRejectsOverLimit(t *testing.T) {
	handlerCalls := 0
	handler := RateLimit(RateLimitConfig{
		Rate:  rate.Limit(1),
		Burst: 1,
	})(func(core.Invocation) error {
		handlerCalls++
		return nil
	})

	ctx := newRateLimitTestInvocation(newRateLimitTestSession(1))
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

func TestRateLimitSharesBucketAcrossSessions(t *testing.T) {
	handler := RateLimit(RateLimitConfig{
		Rate:  rate.Limit(1),
		Burst: 1,
	})(func(core.Invocation) error {
		return nil
	})

	ctx1 := newRateLimitTestInvocation(newRateLimitTestSession(1))
	ctx2 := newRateLimitTestInvocation(newRateLimitTestSession(2))

	if err := handler(ctx1); err != nil {
		t.Fatalf("first session should pass: %v", err)
	}
	if err := handler(ctx2); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("second session should share global limit, got %v", err)
	}
}

func TestRateLimitHonorsBurst(t *testing.T) {
	handlerCalls := 0
	handler := RateLimit(RateLimitConfig{
		Rate:  rate.Limit(1),
		Burst: 2,
	})(func(core.Invocation) error {
		handlerCalls++
		return nil
	})

	ctx := newRateLimitTestInvocation(newRateLimitTestSession(1))
	for i := 0; i < 2; i++ {
		if err := handler(ctx); err != nil {
			t.Fatalf("call %d should pass within burst: %v", i+1, err)
		}
	}
	if err := handler(ctx); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("third call should be rate limited, got %v", err)
	}
	if handlerCalls != 2 {
		t.Fatalf("handler calls = %d, want 2", handlerCalls)
	}
}

func TestConnRateLimitIsPerSession(t *testing.T) {
	handler := ConnRateLimit(RateLimitConfig{
		Rate:  rate.Limit(1),
		Burst: 1,
	})(func(core.Invocation) error {
		return nil
	})

	session1 := newRateLimitTestSession(1)
	session2 := newRateLimitTestSession(2)
	t.Cleanup(func() {
		_ = session1.Close()
		_ = session2.Close()
	})
	ctx1 := newRateLimitTestInvocation(session1)
	ctx2 := newRateLimitTestInvocation(session2)

	if err := handler(ctx1); err != nil {
		t.Fatalf("first session first call should pass: %v", err)
	}
	if err := handler(ctx1); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("first session second call should be rate limited, got %v", err)
	}
	if err := handler(ctx2); err != nil {
		t.Fatalf("second session first call should pass: %v", err)
	}
}

func TestConnRateLimitRemovesLimiterWhenSessionCloses(t *testing.T) {
	handler := ConnRateLimit(RateLimitConfig{
		Rate:  rate.Limit(0.001),
		Burst: 1,
	})(func(core.Invocation) error {
		return nil
	})

	session := newRateLimitTestSession(9)
	ctx := newRateLimitTestInvocation(session)
	if err := handler(ctx); err != nil {
		t.Fatalf("first call should pass: %v", err)
	}
	if err := handler(ctx); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("second call should be rate limited, got %v", err)
	}

	if err := session.Close(); err != nil {
		t.Fatalf("close session: %v", err)
	}

	reusedIDSession := newRateLimitTestSession(9)
	t.Cleanup(func() {
		_ = reusedIDSession.Close()
	})
	reusedIDCtx := newRateLimitTestInvocation(reusedIDSession)

	waitForRateLimitCleanup(t, func() bool {
		return handler(reusedIDCtx) == nil
	})
}

func TestRateLimitUsesDefaults(t *testing.T) {
	handler := RateLimit(RateLimitConfig{})(func(core.Invocation) error {
		return nil
	})

	ctx := newRateLimitTestInvocation(newRateLimitTestSession(1))
	if err := handler(ctx); err != nil {
		t.Fatalf("default rate limit should allow first request: %v", err)
	}
}

func waitForRateLimitCleanup(t *testing.T, condition func() bool) {
	t.Helper()

	deadline := time.After(time.Second)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()

	for {
		if condition() {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline:
			t.Fatal("timed out waiting for limiter cleanup")
		}
	}
}

func newRateLimitTestInvocation(session *rateLimitTestSession) core.Invocation {
	return core.NewMessageInvocation(context.Background(), core.NewContext(session, core.Message{ID: core.MsgEcho}))
}

type rateLimitTestSession struct {
	id   uint64
	done chan struct{}
	once sync.Once
}

func newRateLimitTestSession(id uint64) *rateLimitTestSession {
	return &rateLimitTestSession{
		id:   id,
		done: make(chan struct{}),
	}
}

func (s *rateLimitTestSession) ID() uint64 {
	return s.id
}

func (s *rateLimitTestSession) Transport() core.Transport {
	return core.TransportTCP
}

func (s *rateLimitTestSession) RemoteAddr() net.Addr {
	return nil
}

func (s *rateLimitTestSession) Send(core.Message) error {
	return nil
}

func (s *rateLimitTestSession) Close() error {
	s.once.Do(func() {
		close(s.done)
	})
	return nil
}

func (s *rateLimitTestSession) Done() <-chan struct{} {
	return s.done
}
