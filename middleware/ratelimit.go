// File: middleware/ratelimit.go
// Purpose: Defines or tests reusable KPro invocation middleware.

package middleware

import (
	"errors"
	"sync"

	"github.com/saramand9/bignet/kpro/core"
	"golang.org/x/time/rate"
)

// ErrRateLimited is returned when a rate limiter rejects an invocation.
var ErrRateLimited = errors.New("rate limit exceeded")

// RateLimitConfig configures token-bucket middleware limits.
type RateLimitConfig struct {
	// Rate is the allowed event rate. Values <= 0 use a default of 1000.
	Rate rate.Limit
	// Burst is the token bucket burst size. Values <= 0 derive from Rate.
	Burst int
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
			ctx.Set(core.PacketLimiterKey, core.PacketLimiter(func() error {
				if !limiter.Allow() {
					return ErrRateLimited
				}
				return nil
			}))
			if !limiter.Allow() {
				return ErrRateLimited
			}
			return next(ctx)
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
