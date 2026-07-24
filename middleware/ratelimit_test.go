// File: middleware/ratelimit_test.go
// Purpose: Defines or tests reusable KPro invocation middleware.

package middleware

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/saramand9/kpro/core"
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

func TestConnRateLimitSeparatesTransportsWithSameConnectionID(t *testing.T) {
	handler := ConnRateLimit(RateLimitConfig{
		Rate:  rate.Limit(0.001),
		Burst: 1,
	})(func(core.Invocation) error { return nil })
	tcpDone := make(chan struct{})
	kcpDone := make(chan struct{})
	t.Cleanup(func() {
		close(tcpDone)
		close(kcpDone)
	})
	tcpInvocation := core.NewInvocation(core.InvocationOptions{
		Transport: core.TransportTCP,
		ConnID:    1,
		ConnDone:  tcpDone,
	})
	kcpInvocation := core.NewInvocation(core.InvocationOptions{
		Transport: core.TransportKCP,
		ConnID:    1,
		ConnDone:  kcpDone,
	})

	if err := handler(tcpInvocation); err != nil {
		t.Fatalf("first TCP call: %v", err)
	}
	if err := handler(kcpInvocation); err != nil {
		t.Fatalf("same id on KCP should use an independent bucket: %v", err)
	}
}

func TestConnRateLimitSeparatesEndpointLifetimesWithSameConnectionID(t *testing.T) {
	handler := ConnRateLimit(RateLimitConfig{
		Rate:  rate.Limit(0.001),
		Burst: 1,
	})(func(core.Invocation) error { return nil })
	firstDone := make(chan struct{})
	secondDone := make(chan struct{})
	t.Cleanup(func() {
		close(firstDone)
		close(secondDone)
	})
	first := core.NewInvocation(core.InvocationOptions{
		Transport: core.TransportTCP,
		ConnID:    1,
		ConnDone:  firstDone,
	})
	second := core.NewInvocation(core.InvocationOptions{
		Transport: core.TransportTCP,
		ConnID:    1,
		ConnDone:  secondDone,
	})

	if err := handler(first); err != nil {
		t.Fatalf("first endpoint call: %v", err)
	}
	if err := handler(second); err != nil {
		t.Fatalf("same local id on another endpoint should use an independent bucket: %v", err)
	}
}

func TestConnRateLimitSurvivesInvocationContextReplacement(t *testing.T) {
	connectionDone := make(chan struct{})
	t.Cleanup(func() { close(connectionDone) })
	withRequestContext := func(next core.InvocationHandler) core.InvocationHandler {
		return func(invocation core.Invocation) error {
			requestCtx, cancel := context.WithCancel(invocation.Context())
			defer cancel()
			return next(core.WithInvocationContext(invocation, requestCtx))
		}
	}
	handler := core.Chain(
		withRequestContext,
		ConnRateLimit(RateLimitConfig{Rate: rate.Limit(0.001), Burst: 1}),
	)(func(core.Invocation) error { return nil })

	newRequest := func() core.Invocation {
		return core.NewInvocation(core.InvocationOptions{
			Context:   context.Background(),
			Transport: core.TransportHTTP,
			ConnID:    1,
			ConnDone:  connectionDone,
		})
	}
	if err := handler(newRequest()); err != nil {
		t.Fatalf("first request: %v", err)
	}
	// Give an implementation that mistakenly watches the request context time
	// to delete its bucket before the next request on the same connection.
	time.Sleep(5 * time.Millisecond)
	if err := handler(newRequest()); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("second request on same connection = %v, want ErrRateLimited", err)
	}
}

func TestConnRateLimitRemovesIdleBucketWithoutConnectionLifetime(t *testing.T) {
	handler := ConnRateLimit(RateLimitConfig{
		Rate:        rate.Limit(0.001),
		Burst:       1,
		IdleTimeout: time.Millisecond,
	})(func(core.Invocation) error { return nil })
	invocation := core.NewInvocation(core.InvocationOptions{
		Transport: core.TransportTCP,
		ConnID:    1,
	})

	if err := handler(invocation); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if err := handler(invocation); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("second call = %v, want ErrRateLimited", err)
	}
	time.Sleep(20 * time.Millisecond)
	if err := handler(invocation); err != nil {
		t.Fatalf("idle bucket should be recreated: %v", err)
	}
}

func TestConnRateLimitBoundsBucketCardinality(t *testing.T) {
	handler := ConnRateLimit(RateLimitConfig{
		Rate:       rate.Limit(1),
		Burst:      1,
		MaxBuckets: 1,
	})(func(core.Invocation) error { return nil })
	firstDone := make(chan struct{})
	secondDone := make(chan struct{})
	second := core.NewInvocation(core.InvocationOptions{
		Transport: core.TransportTCP,
		ConnID:    2,
		ConnDone:  secondDone,
	})
	t.Cleanup(func() { close(secondDone) })

	if err := handler(core.NewInvocation(core.InvocationOptions{
		Transport: core.TransportTCP,
		ConnID:    1,
		ConnDone:  firstDone,
	})); err != nil {
		t.Fatalf("first bucket: %v", err)
	}
	if err := handler(second); !errors.Is(err, core.ErrOverloaded) {
		t.Fatalf("new key over bucket bound = %v, want core.ErrOverloaded", err)
	}
	close(firstDone)
	waitForRateLimitCleanup(t, func() bool { return handler(second) == nil })
}

func TestIPRateLimitSharesBucketAcrossPorts(t *testing.T) {
	handler := IPRateLimit(RateLimitConfig{
		Rate:        rate.Limit(0.001),
		Burst:       1,
		IdleTimeout: time.Second,
	})(func(core.Invocation) error {
		return nil
	})

	ctx1 := core.NewInvocation(core.InvocationOptions{Remote: "127.0.0.1:1000"})
	ctx2 := core.NewInvocation(core.InvocationOptions{Remote: "127.0.0.1:2000"})

	if err := handler(ctx1); err != nil {
		t.Fatalf("first port should pass: %v", err)
	}
	if err := handler(ctx2); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("same IP on another port should be rate limited, got %v", err)
	}
}

func TestIPRateLimitSeparatesIPs(t *testing.T) {
	handler := IPRateLimit(RateLimitConfig{
		Rate:        rate.Limit(0.001),
		Burst:       1,
		IdleTimeout: time.Second,
	})(func(core.Invocation) error {
		return nil
	})

	ctx1 := core.NewInvocation(core.InvocationOptions{Remote: "127.0.0.1:1000"})
	ctx2 := core.NewInvocation(core.InvocationOptions{Remote: "127.0.0.2:1000"})

	if err := handler(ctx1); err != nil {
		t.Fatalf("first IP should pass: %v", err)
	}
	if err := handler(ctx2); err != nil {
		t.Fatalf("different IP should have an independent bucket: %v", err)
	}
}

func TestIPRateLimitParsesIPv6HostPort(t *testing.T) {
	handler := IPRateLimit(RateLimitConfig{
		Rate:        rate.Limit(0.001),
		Burst:       1,
		IdleTimeout: time.Second,
	})(func(core.Invocation) error {
		return nil
	})

	ctx1 := core.NewInvocation(core.InvocationOptions{Remote: "[::1]:1000"})
	ctx2 := core.NewInvocation(core.InvocationOptions{Remote: "[::1]:2000"})

	if err := handler(ctx1); err != nil {
		t.Fatalf("first IPv6 port should pass: %v", err)
	}
	if err := handler(ctx2); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("same IPv6 on another port should be rate limited, got %v", err)
	}
}

func TestIPRateLimitRemovesIdleLimiter(t *testing.T) {
	handler := IPRateLimit(RateLimitConfig{
		Rate:        rate.Limit(0.001),
		Burst:       1,
		IdleTimeout: time.Millisecond,
	})(func(core.Invocation) error {
		return nil
	})

	ctx := core.NewInvocation(core.InvocationOptions{Remote: "127.0.0.1:1000"})
	if err := handler(ctx); err != nil {
		t.Fatalf("first call should pass: %v", err)
	}
	if err := handler(ctx); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("second call should be rate limited, got %v", err)
	}

	time.Sleep(2 * time.Millisecond)
	if err := handler(ctx); err != nil {
		t.Fatalf("idle bucket should be recreated: %v", err)
	}
}

func TestPacketLimitersComposeAcrossRateLimiters(t *testing.T) {
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })

	ctx := core.NewInvocation(core.InvocationOptions{
		ConnID: 7,
		Remote: "127.0.0.1:1000",
		Done:   done,
	})
	handler := core.Chain(
		IPRateLimit(RateLimitConfig{Rate: rate.Limit(0.001), Burst: 1, IdleTimeout: time.Second}),
		ConnRateLimit(RateLimitConfig{Rate: rate.Limit(0.001), Burst: 2}),
	)(func(core.Invocation) error {
		return nil
	})

	if err := handler(ctx); err != nil {
		t.Fatalf("first call should pass: %v", err)
	}
	value, ok := ctx.Get(core.PacketLimiterKey)
	if !ok {
		t.Fatal("packet limiter was not stored")
	}
	limiter, ok := value.(core.PacketLimiter)
	if !ok {
		t.Fatalf("packet limiter type = %T, want core.PacketLimiter", value)
	}
	if err := limiter(); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("packet limiter should include exhausted IP limiter, got %v", err)
	}
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
	return core.NewMessageInvocation(context.Background(), core.NewContext(session, core.Message{ID: testMsgEcho}))
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
