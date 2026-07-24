// Package prometheusx provides opt-in Prometheus metrics for KPro invocations.
// It deliberately uses bounded labels: transport, stable operation, and result.
package prometheusx

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/saramand9/kpro/core"
)

const defaultNamespace = "kpro"

// Config controls metric registration and exposition.
type Config struct {
	Registerer      prometheus.Registerer
	Gatherer        prometheus.Gatherer
	Namespace       string
	Subsystem       string
	DurationBuckets []float64
}

// Metrics owns registered collectors and invocation middleware.
type Metrics struct {
	invocations *prometheus.CounterVec
	inflight    *prometheus.GaugeVec
	duration    *prometheus.HistogramVec
	gatherer    prometheus.Gatherer
}

// New registers a new isolated collector set. Applications that create more
// than one set should provide separate registries or distinct subsystems.
func New(config ...Config) (*Metrics, error) {
	cfg := normalizeConfig(config)
	metrics := &Metrics{
		invocations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: cfg.Namespace,
			Subsystem: cfg.Subsystem,
			Name:      "invocations_total",
			Help:      "Completed KPro invocations partitioned by result.",
		}, []string{"transport", "operation", "result"}),
		inflight: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: cfg.Namespace,
			Subsystem: cfg.Subsystem,
			Name:      "invocations_inflight",
			Help:      "KPro invocations currently executing.",
		}, []string{"transport", "operation"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: cfg.Namespace,
			Subsystem: cfg.Subsystem,
			Name:      "invocation_duration_seconds",
			Help:      "KPro invocation handler duration in seconds.",
			Buckets:   cfg.DurationBuckets,
		}, []string{"transport", "operation"}),
		gatherer: cfg.Gatherer,
	}
	collectors := []prometheus.Collector{metrics.invocations, metrics.inflight, metrics.duration}
	registered := make([]prometheus.Collector, 0, len(collectors))
	for _, collector := range collectors {
		if err := cfg.Registerer.Register(collector); err != nil {
			for _, previous := range registered {
				cfg.Registerer.Unregister(previous)
			}
			return nil, err
		}
		registered = append(registered, collector)
	}
	return metrics, nil
}

// MustNew is New with panic-on-registration-error semantics.
func MustNew(config ...Config) *Metrics {
	metrics, err := New(config...)
	if err != nil {
		panic(err)
	}
	return metrics
}

// Middleware records completion result, in-flight work, and duration.
func (m *Metrics) Middleware() core.Middleware {
	return func(next core.InvocationHandler) core.InvocationHandler {
		return func(invocation core.Invocation) (err error) {
			transport := string(invocation.Transport())
			operation := invocation.Operation()
			inflight := m.inflight.WithLabelValues(transport, operation)
			duration := m.duration.WithLabelValues(transport, operation)
			started := time.Now()
			inflight.Inc()
			defer func() {
				inflight.Dec()
				duration.Observe(time.Since(started).Seconds())
				if recovered := recover(); recovered != nil {
					m.invocations.WithLabelValues(transport, operation, "panic").Inc()
					panic(recovered)
				}
				result := "ok"
				if err != nil {
					result = "error"
				}
				m.invocations.WithLabelValues(transport, operation, result).Inc()
			}()
			return next(invocation)
		}
	}
}

// Handler returns a Prometheus exposition handler for this metric registry.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.gatherer, promhttp.HandlerOpts{})
}

func normalizeConfig(config []Config) Config {
	var cfg Config
	if len(config) > 0 {
		cfg = config[0]
	}
	if cfg.Registerer == nil {
		cfg.Registerer = prometheus.DefaultRegisterer
	}
	if cfg.Gatherer == nil {
		if gatherer, ok := cfg.Registerer.(prometheus.Gatherer); ok {
			cfg.Gatherer = gatherer
		} else {
			cfg.Gatherer = prometheus.DefaultGatherer
		}
	}
	if cfg.Namespace == "" {
		cfg.Namespace = defaultNamespace
	}
	if len(cfg.DurationBuckets) == 0 {
		cfg.DurationBuckets = prometheus.DefBuckets
	}
	return cfg
}
