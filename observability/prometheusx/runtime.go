package prometheusx

import (
	"errors"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/saramand9/kpro/core"
)

// LifecycleSource is implemented by server.Server, core.Router, and custom
// one-shot components that expose the shared lifecycle state.
type LifecycleSource interface {
	State() core.LifecycleState
}

// RouterSnapshotter is implemented by core.Router and high-level message
// endpoints that expose router admission statistics.
type RouterSnapshotter interface {
	Snapshot() core.RouterStats
}

// RuntimeCollectorConfig controls bounded identity labels and metric names.
type RuntimeCollectorConfig struct {
	Namespace string
	Subsystem string
	Name      string
	Component string
}

// LifecycleCollector exports the current lifecycle as a one-hot labeled gauge.
type LifecycleCollector struct {
	source LifecycleSource

	descriptor *prometheus.Desc
}

// NewLifecycleCollector creates a collector for one named component.
func NewLifecycleCollector(source LifecycleSource, config RuntimeCollectorConfig) (*LifecycleCollector, error) {
	if source == nil {
		return nil, errors.New("lifecycle source is required")
	}
	config = normalizeRuntimeConfig(config)
	if config.Name == "" || config.Component == "" {
		return nil, errors.New("lifecycle collector name and component are required")
	}
	return &LifecycleCollector{
		source: source,
		descriptor: prometheus.NewDesc(
			prometheus.BuildFQName(config.Namespace, config.Subsystem, "lifecycle_state"),
			"Current one-shot lifecycle state.",
			[]string{"state"},
			prometheus.Labels{"component": config.Component, "name": config.Name},
		),
	}, nil
}

// Describe implements prometheus.Collector.
func (c *LifecycleCollector) Describe(channel chan<- *prometheus.Desc) {
	channel <- c.descriptor
}

// Collect implements prometheus.Collector.
func (c *LifecycleCollector) Collect(channel chan<- prometheus.Metric) {
	channel <- prometheus.MustNewConstMetric(c.descriptor, prometheus.GaugeValue, 1, c.source.State().String())
}

// RouterCollector exports queue, admission, and completion snapshots.
type RouterCollector struct {
	source RouterSnapshotter

	lifecycle *prometheus.Desc
	inflight  *prometheus.Desc
	waiting   *prometheus.Desc
	sessions  *prometheus.Desc
	queue     *prometheus.Desc
	dispatch  *prometheus.Desc
}

// NewRouterCollector creates a collector for one endpoint/router name.
func NewRouterCollector(source RouterSnapshotter, config RuntimeCollectorConfig) (*RouterCollector, error) {
	if source == nil {
		return nil, errors.New("router snapshot source is required")
	}
	config = normalizeRuntimeConfig(config)
	if config.Name == "" {
		return nil, errors.New("router collector name is required")
	}
	fqName := func(name string) string {
		return prometheus.BuildFQName(config.Namespace, config.Subsystem, name)
	}
	return &RouterCollector{
		source: source,
		lifecycle: prometheus.NewDesc(fqName("router_lifecycle_state"),
			"Current router lifecycle state.", []string{"state"}, prometheus.Labels{"router": config.Name}),
		inflight: prometheus.NewDesc(fqName("router_inflight"),
			"Accepted router work currently queued or running.", []string{"kind"}, prometheus.Labels{"router": config.Name}),
		waiting: prometheus.NewDesc(fqName("router_waiting_dispatches"),
			"Dispatch callers holding bounded wait capacity.", []string{"kind"}, prometheus.Labels{"router": config.Name}),
		sessions: prometheus.NewDesc(fqName("router_sessions"),
			"Current router session executors.", nil, prometheus.Labels{"router": config.Name}),
		queue: prometheus.NewDesc(fqName("router_queue_depth"),
			"Current router queue depth.", []string{"queue"}, prometheus.Labels{"router": config.Name}),
		dispatch: prometheus.NewDesc(fqName("router_dispatch_total"),
			"Router dispatch counters partitioned by outcome.", []string{"outcome"}, prometheus.Labels{"router": config.Name}),
	}, nil
}

// Describe implements prometheus.Collector.
func (c *RouterCollector) Describe(channel chan<- *prometheus.Desc) {
	channel <- c.lifecycle
	channel <- c.inflight
	channel <- c.waiting
	channel <- c.sessions
	channel <- c.queue
	channel <- c.dispatch
}

// Collect implements prometheus.Collector.
func (c *RouterCollector) Collect(channel chan<- prometheus.Metric) {
	snapshot := c.source.Snapshot()
	channel <- prometheus.MustNewConstMetric(c.lifecycle, prometheus.GaugeValue, 1, snapshot.State.String())
	channel <- prometheus.MustNewConstMetric(c.inflight, prometheus.GaugeValue, float64(snapshot.Inflight), "current")
	channel <- prometheus.MustNewConstMetric(c.inflight, prometheus.GaugeValue, float64(snapshot.MaxInflight), "limit")
	channel <- prometheus.MustNewConstMetric(c.waiting, prometheus.GaugeValue, float64(snapshot.WaitingDispatches), "current")
	channel <- prometheus.MustNewConstMetric(c.waiting, prometheus.GaugeValue, float64(snapshot.MaxWaitingDispatches), "global_limit")
	channel <- prometheus.MustNewConstMetric(c.waiting, prometheus.GaugeValue, float64(snapshot.MaxWaitingDispatchesPerSession), "per_session_limit")
	channel <- prometheus.MustNewConstMetric(c.sessions, prometheus.GaugeValue, float64(snapshot.Sessions))
	channel <- prometheus.MustNewConstMetric(c.queue, prometheus.GaugeValue, float64(snapshot.SyncQueueDepth), "sync")
	channel <- prometheus.MustNewConstMetric(c.queue, prometheus.GaugeValue, float64(snapshot.AsyncQueueDepth), "async")
	counters := []struct {
		outcome string
		value   uint64
	}{
		{outcome: "accepted", value: snapshot.Accepted},
		{outcome: "completed", value: snapshot.Completed},
		{outcome: "dropped", value: snapshot.Dropped},
		{outcome: "rejected", value: snapshot.Rejected},
		{outcome: "waited", value: snapshot.Waited},
		{outcome: "global_wait_rejected", value: snapshot.GlobalWaitRejected},
		{outcome: "session_wait_rejected", value: snapshot.SessionWaitRejected},
	}
	for _, counter := range counters {
		channel <- prometheus.MustNewConstMetric(c.dispatch, prometheus.CounterValue, float64(counter.value), counter.outcome)
	}
}

func normalizeRuntimeConfig(config RuntimeCollectorConfig) RuntimeCollectorConfig {
	if config.Namespace == "" {
		config.Namespace = defaultNamespace
	}
	return config
}
