// File: middleware/ratelimit.go
// Purpose: Defines or tests reusable KPro invocation middleware.

package middleware

import (
	"net"
	"strings"
	"sync"
	"time"

	"github.com/saramand9/kpro/core"
	"golang.org/x/time/rate"
)

const (
	// DefaultRateLimitIdleTimeout removes idle key-based buckets after this duration.
	DefaultRateLimitIdleTimeout = time.Minute
	// DefaultIPRateLimitIdleTimeout is retained for compatibility.
	DefaultIPRateLimitIdleTimeout = DefaultRateLimitIdleTimeout
	// DefaultRateLimitMaxBuckets bounds key-based limiter memory by default.
	DefaultRateLimitMaxBuckets = 65536
)

// ErrRateLimited is returned when a rate limiter rejects an invocation.
var ErrRateLimited = core.ErrRateLimited

// RateLimitConfig configures token-bucket middleware limits.
type RateLimitConfig struct {
	// Rate is the allowed event rate. Values <= 0 use a default of 1000.
	Rate rate.Limit
	// Burst is the token bucket burst size. Values <= 0 derive from Rate.
	Burst int
	// IdleTimeout removes idle key-based buckets. Values <= 0 use the default.
	IdleTimeout time.Duration
	// MaxBuckets bounds key-based limiter state. Values <= 0 use the default.
	// Existing buckets continue to work when the bound is reached; new keys are
	// rejected with core.ErrOverloaded until capacity becomes available.
	MaxBuckets int
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

// ConnRateLimit applies one token bucket per physical or virtual connection.
// Transport, local connection id, peer, and connection lifetime form the key,
// so independently numbered endpoints and transports do not share buckets.
func ConnRateLimit(config RateLimitConfig) core.Middleware {
	config = normalizeKeyRateLimitConfig(config)

	var mu sync.Mutex
	limiters := make(map[connLimiterKey]*connLimiterEntry)
	var lastCleanup time.Time

	limiterFor := func(ctx core.Invocation) (*rate.Limiter, error) {
		key := connectionLimiterKey(ctx)
		now := time.Now()

		mu.Lock()
		entry := limiters[key]
		if entry != nil && entry.done == nil && now.Sub(entry.lastSeen) >= config.IdleTimeout {
			delete(limiters, key)
			entry = nil
		}
		if entry != nil {
			entry.lastSeen = now
			limiter := entry.limiter
			mu.Unlock()
			return limiter, nil
		}
		if lastCleanup.IsZero() || now.Sub(lastCleanup) >= config.IdleTimeout {
			pruneConnLimiters(limiters, now, config.IdleTimeout)
			lastCleanup = now
		}
		if len(limiters) >= config.MaxBuckets {
			// A capacity check also performs an immediate prune so closed or idle
			// connections do not wait for the periodic cleanup interval.
			pruneConnLimiters(limiters, now, config.IdleTimeout)
			if len(limiters) >= config.MaxBuckets {
				mu.Unlock()
				return nil, core.ErrOverloaded
			}
		}

		entry = limiters[key]
		if entry == nil {
			entry = &connLimiterEntry{
				limiter:  rate.NewLimiter(config.Rate, config.Burst),
				lastSeen: now,
				done:     key.done,
			}
			limiters[key] = entry

			done := key.done
			if done != nil {
				go func() {
					<-done
					mu.Lock()
					if limiters[key] == entry {
						delete(limiters, key)
					}
					mu.Unlock()
				}()
			}
		}
		limiter := entry.limiter
		mu.Unlock()

		return limiter, nil
	}

	return func(next core.InvocationHandler) core.InvocationHandler {
		return func(ctx core.Invocation) error {
			limiter, err := limiterFor(ctx)
			if err != nil {
				return err
			}
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
	config = normalizeKeyRateLimitConfig(config)

	var mu sync.Mutex
	limiters := make(map[string]*ipLimiterEntry)
	var lastCleanup time.Time

	limiterFor := func(ctx core.Invocation) (*rate.Limiter, error) {
		key := remoteIPKey(ctx.RemoteAddr())
		now := time.Now()

		mu.Lock()
		if lastCleanup.IsZero() || now.Sub(lastCleanup) >= config.IdleTimeout {
			pruneIdleIPLimiters(limiters, now, config.IdleTimeout)
			lastCleanup = now
		}
		entry := limiters[key]
		if entry == nil {
			if len(limiters) >= config.MaxBuckets {
				pruneIdleIPLimiters(limiters, now, config.IdleTimeout)
				if len(limiters) >= config.MaxBuckets {
					mu.Unlock()
					return nil, core.ErrOverloaded
				}
			}
			entry = &ipLimiterEntry{
				limiter: rate.NewLimiter(config.Rate, config.Burst),
			}
			limiters[key] = entry
		}
		entry.lastSeen = now
		limiter := entry.limiter
		mu.Unlock()

		return limiter, nil
	}

	return func(next core.InvocationHandler) core.InvocationHandler {
		return func(ctx core.Invocation) error {
			limiter, err := limiterFor(ctx)
			if err != nil {
				return err
			}
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

type connLimiterKey struct {
	transport core.Transport
	id        uint64
	remote    string
	done      <-chan struct{}
}

type connLimiterEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
	done     <-chan struct{}
}

func connectionLimiterKey(invocation core.Invocation) connLimiterKey {
	return connLimiterKey{
		transport: invocation.Transport(),
		id:        invocation.ConnID(),
		remote:    invocation.RemoteAddr(),
		done:      core.InvocationConnDone(invocation),
	}
}

func pruneConnLimiters(limiters map[connLimiterKey]*connLimiterEntry, now time.Time, idleTimeout time.Duration) {
	for key, entry := range limiters {
		if entry.done != nil {
			select {
			case <-entry.done:
				delete(limiters, key)
			default:
			}
			continue
		}
		if now.Sub(entry.lastSeen) >= idleTimeout {
			delete(limiters, key)
		}
	}
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

func normalizeKeyRateLimitConfig(config RateLimitConfig) RateLimitConfig {
	config = normalizeRateLimitConfig(config)
	if config.IdleTimeout <= 0 {
		config.IdleTimeout = DefaultRateLimitIdleTimeout
	}
	if config.MaxBuckets <= 0 {
		config.MaxBuckets = DefaultRateLimitMaxBuckets
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
