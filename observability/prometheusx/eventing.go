package prometheusx

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/saramand9/bignet/kpro/eventing/delivery"
	"github.com/saramand9/bignet/kpro/eventing/projector"
)

const (
	defaultEventingSubsystem = "eventing"
	defaultEventingTimeout   = 5 * time.Second
)

// EventingCollectorConfig controls eventing metric names and backlog scrape
// latency. Timeout bounds the complete concurrent backlog collection pass.
type EventingCollectorConfig struct {
	Namespace string
	Subsystem string
	Timeout   time.Duration
}

// BacklogSource is the narrow delivery capability required by
// BacklogCollector.
type BacklogSource interface {
	Backlog(ctx context.Context, consumer string) (delivery.BacklogStats, error)
}

// ProjectorSource is the narrow runner capability required by
// ProjectorCollector.
type ProjectorSource interface {
	Snapshot() projector.Snapshot
	Health() error
}

// BacklogCollector exports bounded delivery backlog metrics for the explicit
// consumer names supplied at construction.
type BacklogCollector struct {
	source    BacklogSource
	consumers []string
	timeout   time.Duration
	running   map[string]chan struct{}
	backlog   *prometheus.Desc
	scrapeOK  *prometheus.Desc
}

// NewBacklogCollector creates an unregistered collector. Register the returned
// value with the application's Prometheus registry.
func NewBacklogCollector(source BacklogSource, consumers []string, config ...EventingCollectorConfig) (*BacklogCollector, error) {
	if source == nil {
		return nil, fmt.Errorf("eventing backlog source is required")
	}
	names, err := boundedNames("consumer", consumers)
	if err != nil {
		return nil, err
	}
	cfg := normalizeEventingCollectorConfig(config)
	collector := &BacklogCollector{
		source:    source,
		consumers: names,
		timeout:   cfg.Timeout,
		running:   make(map[string]chan struct{}, len(names)),
		backlog: prometheus.NewDesc(
			prometheus.BuildFQName(cfg.Namespace, cfg.Subsystem, "delivery_backlog"),
			"Current event delivery backlog partitioned by bounded status.",
			[]string{"consumer", "status"},
			nil,
		),
		scrapeOK: prometheus.NewDesc(
			prometheus.BuildFQName(cfg.Namespace, cfg.Subsystem, "delivery_backlog_scrape_success"),
			"Whether the latest delivery backlog collection completed successfully.",
			[]string{"consumer"},
			nil,
		),
	}
	for _, consumer := range names {
		collector.running[consumer] = make(chan struct{}, 1)
	}
	return collector, nil
}

// Describe implements prometheus.Collector.
func (c *BacklogCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.backlog
	ch <- c.scrapeOK
}

// Collect implements prometheus.Collector.
func (c *BacklogCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()

	type result struct {
		consumer string
		stats    delivery.BacklogStats
		err      error
	}
	results := make(chan result, len(c.consumers))
	for _, consumer := range c.consumers {
		consumer := consumer
		running := c.running[consumer]
		select {
		case running <- struct{}{}:
		default:
			results <- result{consumer: consumer, err: fmt.Errorf("previous backlog collection is still running")}
			continue
		}
		go func() {
			defer func() { <-running }()
			stats, err := c.source.Backlog(ctx, consumer)
			results <- result{consumer: consumer, stats: stats, err: err}
		}()
	}

	pending := make(map[string]struct{}, len(c.consumers))
	for _, consumer := range c.consumers {
		pending[consumer] = struct{}{}
	}
	for len(pending) > 0 {
		select {
		case result := <-results:
			if _, ok := pending[result.consumer]; !ok {
				continue
			}
			delete(pending, result.consumer)
			if result.err != nil {
				ch <- prometheus.MustNewConstMetric(c.scrapeOK, prometheus.GaugeValue, 0, result.consumer)
				continue
			}
			ch <- prometheus.MustNewConstMetric(c.scrapeOK, prometheus.GaugeValue, 1, result.consumer)
			c.collectStats(ch, result.consumer, result.stats)
		case <-ctx.Done():
			for consumer := range pending {
				ch <- prometheus.MustNewConstMetric(c.scrapeOK, prometheus.GaugeValue, 0, consumer)
			}
			return
		}
	}
}

func (c *BacklogCollector) collectStats(ch chan<- prometheus.Metric, consumer string, stats delivery.BacklogStats) {
	values := []struct {
		status string
		value  int64
	}{
		{status: string(delivery.StatusPending), value: stats.Pending},
		{status: string(delivery.StatusInflight), value: stats.Inflight},
		{status: string(delivery.StatusFailed), value: stats.Failed},
		{status: string(delivery.StatusDead), value: stats.Dead},
	}
	for _, value := range values {
		ch <- prometheus.MustNewConstMetric(c.backlog, prometheus.GaugeValue, float64(value.value), consumer, value.status)
	}
}

// ProjectorCollector exports snapshots and health for explicitly named
// projector sources. No metric label is derived from errors or event data.
type ProjectorCollector struct {
	sources []namedProjectorSource

	running           *prometheus.Desc
	healthy           *prometheus.Desc
	consecutiveErrors *prometheus.Desc
	startedAt         *prometheus.Desc
	stoppedAt         *prometheus.Desc
	lastPassAt        *prometheus.Desc
	lastSuccessAt     *prometheus.Desc
	lastErrorAt       *prometheus.Desc
	totalPasses       *prometheus.Desc
	totalProcessed    *prometheus.Desc
	totalErrors       *prometheus.Desc
}

type namedProjectorSource struct {
	name   string
	source ProjectorSource
}

// NewProjectorCollector creates an unregistered collector. Map keys are the
// complete, caller-controlled set of projector label values.
func NewProjectorCollector(sources map[string]ProjectorSource, config ...EventingCollectorConfig) (*ProjectorCollector, error) {
	if len(sources) == 0 {
		return nil, fmt.Errorf("at least one projector source is required")
	}
	names := make([]string, 0, len(sources))
	for name, source := range sources {
		if name == "" {
			return nil, fmt.Errorf("projector name is required")
		}
		if source == nil {
			return nil, fmt.Errorf("projector source %q is required", name)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	entries := make([]namedProjectorSource, 0, len(names))
	for _, name := range names {
		entries = append(entries, namedProjectorSource{name: name, source: sources[name]})
	}

	cfg := normalizeEventingCollectorConfig(config)
	desc := func(name, help string) *prometheus.Desc {
		return prometheus.NewDesc(
			prometheus.BuildFQName(cfg.Namespace, cfg.Subsystem, name),
			help,
			[]string{"projector"},
			nil,
		)
	}
	return &ProjectorCollector{
		sources: entries,
		running: desc(
			"projector_running",
			"Whether the projector runner is currently running.",
		),
		healthy: desc(
			"projector_healthy",
			"Whether the projector runner reports healthy.",
		),
		consecutiveErrors: desc(
			"projector_consecutive_errors",
			"Number of consecutive failed projector passes.",
		),
		startedAt: desc(
			"projector_started_timestamp_seconds",
			"Unix timestamp when the projector runner first started.",
		),
		stoppedAt: desc(
			"projector_stopped_timestamp_seconds",
			"Unix timestamp when the projector runner most recently stopped.",
		),
		lastPassAt: desc(
			"projector_last_pass_timestamp_seconds",
			"Unix timestamp of the latest projector pass.",
		),
		lastSuccessAt: desc(
			"projector_last_success_timestamp_seconds",
			"Unix timestamp of the latest successful projector pass.",
		),
		lastErrorAt: desc(
			"projector_last_error_timestamp_seconds",
			"Unix timestamp of the latest failed projector pass.",
		),
		totalPasses: desc(
			"projector_passes_total",
			"Total number of completed projector passes.",
		),
		totalProcessed: desc(
			"projector_processed_total",
			"Total number of delivery records observed by projector passes.",
		),
		totalErrors: desc(
			"projector_errors_total",
			"Total number of failed projector passes.",
		),
	}, nil
}

// Describe implements prometheus.Collector.
func (c *ProjectorCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.running
	ch <- c.healthy
	ch <- c.consecutiveErrors
	ch <- c.startedAt
	ch <- c.stoppedAt
	ch <- c.lastPassAt
	ch <- c.lastSuccessAt
	ch <- c.lastErrorAt
	ch <- c.totalPasses
	ch <- c.totalProcessed
	ch <- c.totalErrors
}

// Collect implements prometheus.Collector.
func (c *ProjectorCollector) Collect(ch chan<- prometheus.Metric) {
	for _, entry := range c.sources {
		snapshot := entry.source.Snapshot()
		running := 0.0
		if snapshot.Running {
			running = 1
		}
		healthy := 1.0
		if entry.source.Health() != nil {
			healthy = 0
		}
		label := []string{entry.name}
		ch <- prometheus.MustNewConstMetric(c.running, prometheus.GaugeValue, running, label...)
		ch <- prometheus.MustNewConstMetric(c.healthy, prometheus.GaugeValue, healthy, label...)
		ch <- prometheus.MustNewConstMetric(c.consecutiveErrors, prometheus.GaugeValue, float64(snapshot.ConsecutiveErrors), label...)
		ch <- prometheus.MustNewConstMetric(c.startedAt, prometheus.GaugeValue, timestampSeconds(snapshot.StartedAt), label...)
		ch <- prometheus.MustNewConstMetric(c.stoppedAt, prometheus.GaugeValue, timestampSeconds(snapshot.StoppedAt), label...)
		ch <- prometheus.MustNewConstMetric(c.lastPassAt, prometheus.GaugeValue, timestampSeconds(snapshot.LastPassAt), label...)
		ch <- prometheus.MustNewConstMetric(c.lastSuccessAt, prometheus.GaugeValue, timestampSeconds(snapshot.LastSuccessAt), label...)
		ch <- prometheus.MustNewConstMetric(c.lastErrorAt, prometheus.GaugeValue, timestampSeconds(snapshot.LastErrorAt), label...)
		ch <- prometheus.MustNewConstMetric(c.totalPasses, prometheus.CounterValue, float64(snapshot.TotalPasses), label...)
		ch <- prometheus.MustNewConstMetric(c.totalProcessed, prometheus.CounterValue, float64(snapshot.TotalProcessed), label...)
		ch <- prometheus.MustNewConstMetric(c.totalErrors, prometheus.CounterValue, float64(snapshot.TotalErrors), label...)
	}
}

func normalizeEventingCollectorConfig(config []EventingCollectorConfig) EventingCollectorConfig {
	var cfg EventingCollectorConfig
	if len(config) > 0 {
		cfg = config[0]
	}
	if cfg.Namespace == "" {
		cfg.Namespace = defaultNamespace
	}
	if cfg.Subsystem == "" {
		cfg.Subsystem = defaultEventingSubsystem
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultEventingTimeout
	}
	return cfg
}

func boundedNames(kind string, names []string) ([]string, error) {
	if len(names) == 0 {
		return nil, fmt.Errorf("at least one %s is required", kind)
	}
	seen := make(map[string]struct{}, len(names))
	result := append([]string(nil), names...)
	for _, name := range result {
		if name == "" {
			return nil, fmt.Errorf("%s name is required", kind)
		}
		if _, ok := seen[name]; ok {
			return nil, fmt.Errorf("duplicate %s %q", kind, name)
		}
		seen[name] = struct{}{}
	}
	sort.Strings(result)
	return result, nil
}

func timestampSeconds(value time.Time) float64 {
	if value.IsZero() {
		return 0
	}
	return float64(value.UnixNano()) / float64(time.Second)
}
