package middleware

import (
	"errors"
	"sync"

	"github.com/saramand9/bignet/kpro/core"
	"golang.org/x/time/rate"
)

var ErrRateLimited = errors.New("rate limit exceeded")

type RateLimitConfig struct {
	Rate  rate.Limit
	Burst int
}

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
			if !limiterFor(ctx).Allow() {
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
