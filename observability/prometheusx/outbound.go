package prometheusx

import (
	"errors"

	"github.com/prometheus/client_golang/prometheus"
	transportapi "github.com/saramand9/kpro/transport"
)

// OutboundCollector exports bounded transport-level write and drain state.
// Labels are fixed at construction; sessions, peers, and error text are never
// used as time-series dimensions.
type OutboundCollector struct {
	source transportapi.OutboundSnapshotter

	sessions             *prometheus.Desc
	saturatedSessions    *prometheus.Desc
	pendingWrites        *prometheus.Desc
	pendingBytes         *prometheus.Desc
	waitingWriters       *prometheus.Desc
	acceptedWrites       *prometheus.Desc
	acceptedBytes        *prometheus.Desc
	writeResults         *prometheus.Desc
	writeResultBytes     *prometheus.Desc
	drains               *prometheus.Desc
	drainDurationTotal   *prometheus.Desc
	lastDrainDuration    *prometheus.Desc
	maximumDrainDuration *prometheus.Desc
}

// NewOutboundCollector creates an aggregate collector for one message
// transport endpoint. Config Name and Component are required stable values;
// Component normally contains the transport name.
func NewOutboundCollector(source transportapi.OutboundSnapshotter, config RuntimeCollectorConfig) (*OutboundCollector, error) {
	if source == nil {
		return nil, errors.New("outbound snapshot source is required")
	}
	config = normalizeRuntimeConfig(config)
	if config.Name == "" || config.Component == "" {
		return nil, errors.New("outbound collector name and component are required")
	}
	labels := prometheus.Labels{"name": config.Name, "transport": config.Component}
	desc := func(name, help string, variableLabels ...string) *prometheus.Desc {
		return prometheus.NewDesc(
			prometheus.BuildFQName(config.Namespace, config.Subsystem, name),
			help,
			variableLabels,
			labels,
		)
	}
	return &OutboundCollector{
		source: source,
		sessions: desc("outbound_sessions",
			"Current transport sessions with an outbound queue."),
		saturatedSessions: desc("outbound_saturated_sessions",
			"Current outbound sessions at or above their high watermark."),
		pendingWrites: desc("outbound_pending_writes",
			"Current outbound reservations, queued writes, and writer-owned writes."),
		pendingBytes: desc("outbound_pending_bytes",
			"Current bytes retained by outbound reservations, queued writes, and writer-owned writes."),
		waitingWriters: desc("outbound_waiting_writers",
			"Current callers waiting for advisory outbound writability."),
		acceptedWrites: desc("outbound_accepted_writes_total",
			"Total writes accepted into transport-owned outbound state."),
		acceptedBytes: desc("outbound_accepted_bytes_total",
			"Total encoded bytes accepted into transport-owned outbound state."),
		writeResults: desc("outbound_write_results_total",
			"Terminal accepted write results partitioned by bounded outcome.", "result"),
		writeResultBytes: desc("outbound_write_result_bytes_total",
			"Terminal accepted write bytes partitioned by bounded outcome.", "result"),
		drains: desc("outbound_drains_total",
			"Completed transport drain attempts partitioned by bounded outcome.", "result"),
		drainDurationTotal: desc("outbound_drain_duration_seconds_total",
			"Cumulative duration of completed transport drain attempts."),
		lastDrainDuration: desc("outbound_last_drain_duration_seconds",
			"Duration of the most recently completed transport drain attempt."),
		maximumDrainDuration: desc("outbound_max_drain_duration_seconds",
			"Maximum duration of a completed transport drain attempt."),
	}, nil
}

// Describe implements prometheus.Collector.
func (c *OutboundCollector) Describe(channel chan<- *prometheus.Desc) {
	channel <- c.sessions
	channel <- c.saturatedSessions
	channel <- c.pendingWrites
	channel <- c.pendingBytes
	channel <- c.waitingWriters
	channel <- c.acceptedWrites
	channel <- c.acceptedBytes
	channel <- c.writeResults
	channel <- c.writeResultBytes
	channel <- c.drains
	channel <- c.drainDurationTotal
	channel <- c.lastDrainDuration
	channel <- c.maximumDrainDuration
}

// Collect implements prometheus.Collector.
func (c *OutboundCollector) Collect(channel chan<- prometheus.Metric) {
	snapshot := c.source.OutboundSnapshot()
	channel <- prometheus.MustNewConstMetric(c.sessions, prometheus.GaugeValue, float64(snapshot.Sessions))
	channel <- prometheus.MustNewConstMetric(c.saturatedSessions, prometheus.GaugeValue, float64(snapshot.SaturatedSessions))
	channel <- prometheus.MustNewConstMetric(c.pendingWrites, prometheus.GaugeValue, float64(snapshot.PendingWrites))
	channel <- prometheus.MustNewConstMetric(c.pendingBytes, prometheus.GaugeValue, float64(snapshot.PendingBytes))
	channel <- prometheus.MustNewConstMetric(c.waitingWriters, prometheus.GaugeValue, float64(snapshot.WaitingWriters))
	channel <- prometheus.MustNewConstMetric(c.acceptedWrites, prometheus.CounterValue, float64(snapshot.AcceptedWrites))
	channel <- prometheus.MustNewConstMetric(c.acceptedBytes, prometheus.CounterValue, float64(snapshot.AcceptedBytes))

	writeResults := []struct {
		name   string
		writes uint64
		bytes  uint64
	}{
		{name: "success", writes: snapshot.SucceededWrites, bytes: snapshot.SucceededBytes},
		{name: "error", writes: snapshot.FailedWrites, bytes: snapshot.FailedBytes},
		{name: "dropped", writes: snapshot.DroppedWrites, bytes: snapshot.DroppedBytes},
	}
	for _, result := range writeResults {
		channel <- prometheus.MustNewConstMetric(c.writeResults, prometheus.CounterValue, float64(result.writes), result.name)
		channel <- prometheus.MustNewConstMetric(c.writeResultBytes, prometheus.CounterValue, float64(result.bytes), result.name)
	}

	drainResults := []struct {
		name  string
		value uint64
	}{
		{name: "success", value: snapshot.DrainSucceeded},
		{name: "deadline", value: snapshot.DrainDeadline},
		{name: "canceled", value: snapshot.DrainCanceled},
		{name: "error", value: snapshot.DrainFailed},
	}
	for _, result := range drainResults {
		channel <- prometheus.MustNewConstMetric(c.drains, prometheus.CounterValue, float64(result.value), result.name)
	}
	channel <- prometheus.MustNewConstMetric(c.drainDurationTotal, prometheus.CounterValue, snapshot.DrainDurationTotal.Seconds())
	channel <- prometheus.MustNewConstMetric(c.lastDrainDuration, prometheus.GaugeValue, snapshot.LastDrainDuration.Seconds())
	channel <- prometheus.MustNewConstMetric(c.maximumDrainDuration, prometheus.GaugeValue, snapshot.MaxDrainDuration.Seconds())
}
