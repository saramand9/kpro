// File: middleware/ratelimit.go
// Purpose: Defines or tests reusable KPro invocation middleware.

package middleware

import (
	"errors"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/saramand9/bignet/kpro/core"
	"golang.org/x/time/rate"
)

// DefaultIPRateLimitIdleTimeout removes idle IP buckets after this duration.
const DefaultIPRateLimitIdleTimeout = time.Minute

// ErrRateLimited is returned when a rate limiter rejects an invocation.
var ErrRateLimited = errors.New("rate limit exceeded")

// RateLimitConfig configures token-bucket middleware limits.
type RateLimitConfig struct {
	// Rate is the allowed event rate. Values <= 0 use a default of 1000.
	Rate rate.Limit
	// Burst is the token bucket burst size. Values <= 0 derive from Rate.
	Burst int
	// IdleTimeout removes idle key-based buckets. Values <= 0 use the default.
	IdleTimeout time.Duration
}

// RateLimit applies one shared token bucket across all invocations.
func RateLimit(config RateLimitConfig) core.Middleware {
	config = normalizeRateLimitConfig(config)
	limiter := rate.NewLimiter(config.Rate, config.Burst)

	return func(next core.InvocationHandler) core.InvocationHandler {
		return func(ctx core.Invocation) error {
			if !limiter.Allow() {
				return ErrRateLimited
			}
			return next(ctx)
		}
	}
}

// ConnRateLimit applies one token bucket per invocation connection id.
func ConnRateLimit(config RateLimitConfig) core.Middleware {
	config = normalizeRateLimitConfig(config)

	type limiterEntry struct {
		limiter *rate.Limiter
	}

	var mu sync.Mutex
	limiters := make(map[uint64]*limiterEntry)

	limiterFor := func(ctx core.Invocation) *rate.Limiter {
		id := ctx.ConnID()

		mu.Lock()
		entry := limiters[id]
		if entry == nil {
			entry = &limiterEntry{
				limiter: rate.NewLimiter(config.Rate, config.Burst),
			}
			limiters[id] = entry

			done := invocationDone(ctx)
			if done != nil {
				go func() {
					<-done
					mu.Lock()
					if limiters[id] == entry {
						delete(limiters, id)
					}
					mu.Unlock()
				}()
			}
		}
		limiter := entry.limiter
		mu.Unlock()

		return limiter
	}

	return func(next core.InvocationHandler) core.InvocationHandler {
		return func(ctx core.Invocation) error {
			limiter := limiterFor(ctx)
			appendPacketLimiter(ctx, packetLimiter(limiter))
			if !limiter.Allow() {
				return ErrRateLimited
			}
			return next(ctx)
		}
	}
}

// IPRateLimit applies one token bucket per remote IP address.
//
// Remote ports are ignored, so 127.0.0.1:1000 and 127.0.0.1:2000 share
// one bucket. Empty or unparsable remote addresses share a single fallback key.
func IPRateLimit(config RateLimitConfig) core.Middleware {
	config = normalizeIPRateLimitConfig(config)

	var mu sync.Mutex
	limiters := make(map[string]*ipLimiterEntry)
	var lastCleanup time.Time

	limiterFor := func(ctx core.Invocation) *rate.Limiter {
		key := remoteIPKey(ctx.RemoteAddr())
		now := time.Now()

		mu.Lock()
		if lastCleanup.IsZero() || now.Sub(lastCleanup) >= config.IdleTimeout {
			pruneIdleIPLimiters(limiters, now, config.IdleTimeout)
			lastCleanup = now
		}
		entry := limiters[key]
		if entry == nil {
			entry = &ipLimiterEntry{
				limiter: rate.NewLimiter(config.Rate, config.Burst),
			}
			limiters[key] = entry
		}
		entry.lastSeen = now
		limiter := entry.limiter
		mu.Unlock()

		return limiter
	}

	return func(next core.InvocationHandler) core.InvocationHandler {
		return func(ctx core.Invocation) error {
			limiter := limiterFor(ctx)
			appendPacketLimiter(ctx, packetLimiter(limiter))
			if !limiter.Allow() {
				return ErrRateLimited
			}
			return next(ctx)
		}
	}
}

type ipLimiterEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

func pruneIdleIPLimiters(limiters map[string]*ipLimiterEntry, now time.Time, idleTimeout time.Duration) {
	for key, entry := range limiters {
		if now.Sub(entry.lastSeen) >= idleTimeout {
			delete(limiters, key)
		}
	}
}

func normalizeRateLimitConfig(config RateLimitConfig) RateLimitConfig {
	if config.Rate <= 0 {
		config.Rate = rate.Limit(1000)
	}
	if config.Burst <= 0 {
		config.Burst = int(config.Rate)
		if config.Burst < 1 {
			config.Burst = 1
		}
	}
	return config
}

func normalizeIPRateLimitConfig(config RateLimitConfig) RateLimitConfig {
	config = normalizeRateLimitConfig(config)
	if config.IdleTimeout <= 0 {
		config.IdleTimeout = DefaultIPRateLimitIdleTimeout
	}
	return config
}

func appendPacketLimiter(ctx core.Invocation, limiter core.PacketLimiter) {
	if limiter == nil {
		return
	}
	if existing, ok := ctx.Get(core.PacketLimiterKey); ok {
		if previous, ok := existing.(core.PacketLimiter); ok && previous != nil {
			ctx.Set(core.PacketLimiterKey, core.PacketLimiter(func() error {
				if err := previous(); err != nil {
					return err
				}
				return limiter()
			}))
			return
		}
	}
	ctx.Set(core.PacketLimiterKey, limiter)
}

func packetLimiter(limiter *rate.Limiter) core.PacketLimiter {
	return func() error {
		if !limiter.Allow() {
			return ErrRateLimited
		}
		return nil
	}
}

func remoteIPKey(remote string) string {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(remote); err == nil {
		return normalizeIP(host)
	}
	return normalizeIP(remote)
}

func normalizeIP(host string) string {
	host = strings.TrimSpace(host)
	host = strings.TrimPrefix(host, "[")
	host = strings.TrimSuffix(host, "]")
	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	}
	return host
}

func invocationDone(ctx core.Invocation) <-chan struct{} {
	if connDoneCtx, ok := ctx.(interface{ ConnDone() <-chan struct{} }); ok {
		if done := connDoneCtx.ConnDone(); done != nil {
			return done
		}
	}
	if doneCtx, ok := ctx.(interface{ Done() <-chan struct{} }); ok {
		if done := doneCtx.Done(); done != nil {
			return done
		}
	}
	if ctx.Context() == nil {
		return nil
	}
	return ctx.Context().Done()
}
