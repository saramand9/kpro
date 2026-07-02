package middleware

import (
	"encoding/json"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/saramand9/bignet/kpro/core"
)

type Metrics struct {
	started        int64
	completed      int64
	errors         int64
	panics         int64
	inflight       int64
	totalLatencyNS int64
	maxLatencyNS   int64
}

type MetricsSnapshot struct {
	Started        int64         `json:"started"`
	Completed      int64         `json:"completed"`
	Errors         int64         `json:"errors"`
	Panics         int64         `json:"panics"`
	Inflight       int64         `json:"inflight"`
	TotalLatency   time.Duration `json:"total_latency"`
	AverageLatency time.Duration `json:"average_latency"`
	MaxLatency     time.Duration `json:"max_latency"`
}

func NewMetrics() *Metrics {
	return &Metrics{}
}

func (m *Metrics) Middleware() core.Middleware {
	return func(next core.InvocationHandler) core.InvocationHandler {
		return func(ctx core.Invocation) (err error) {
			start := time.Now()
			atomic.AddInt64(&m.started, 1)
			atomic.AddInt64(&m.inflight, 1)
			defer func() {
				elapsed := time.Since(start)
				elapsedNS := int64(elapsed)
				atomic.AddInt64(&m.inflight, -1)
				atomic.AddInt64(&m.completed, 1)
				atomic.AddInt64(&m.totalLatencyNS, elapsedNS)
				m.recordMaxLatency(elapsedNS)

				if recovered := recover(); recovered != nil {
					atomic.AddInt64(&m.panics, 1)
					atomic.AddInt64(&m.errors, 1)
					panic(recovered)
				}
				if err != nil {
					atomic.AddInt64(&m.errors, 1)
				}
			}()
			return next(ctx)
		}
	}
}

func (m *Metrics) Snapshot() MetricsSnapshot {
	started := atomic.LoadInt64(&m.started)
	completed := atomic.LoadInt64(&m.completed)
	totalLatency := time.Duration(atomic.LoadInt64(&m.totalLatencyNS))
	averageLatency := time.Duration(0)
	if completed > 0 {
		averageLatency = time.Duration(int64(totalLatency) / completed)
	}
	return MetricsSnapshot{
		Started:        started,
		Completed:      completed,
		Errors:         atomic.LoadInt64(&m.errors),
		Panics:         atomic.LoadInt64(&m.panics),
		Inflight:       atomic.LoadInt64(&m.inflight),
		TotalLatency:   totalLatency,
		AverageLatency: averageLatency,
		MaxLatency:     time.Duration(atomic.LoadInt64(&m.maxLatencyNS)),
	}
}

func (m *Metrics) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(m.Snapshot())
}

func (m *Metrics) recordMaxLatency(elapsedNS int64) {
	for {
		current := atomic.LoadInt64(&m.maxLatencyNS)
		if elapsedNS <= current {
			return
		}
		if atomic.CompareAndSwapInt64(&m.maxLatencyNS, current, elapsedNS) {
			return
		}
	}
}
