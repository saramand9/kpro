package prometheusx

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	transportapi "github.com/saramand9/kpro/transport"
)

type outboundSnapshotSource struct {
	snapshot transportapi.OutboundSnapshot
}

func (s *outboundSnapshotSource) OutboundSnapshot() transportapi.OutboundSnapshot {
	return s.snapshot
}

func TestOutboundCollectorExportsBoundedAggregateMetrics(t *testing.T) {
	source := &outboundSnapshotSource{snapshot: transportapi.OutboundSnapshot{
		Sessions:           3,
		PendingWrites:      4,
		PendingBytes:       512,
		WaitingWriters:     2,
		SaturatedSessions:  1,
		AcceptedWrites:     20,
		AcceptedBytes:      4096,
		SucceededWrites:    15,
		SucceededBytes:     3000,
		FailedWrites:       2,
		FailedBytes:        512,
		DroppedWrites:      1,
		DroppedBytes:       128,
		DrainSucceeded:     3,
		DrainDeadline:      1,
		DrainCanceled:      2,
		DrainFailed:        4,
		DrainDurationTotal: 2500 * time.Millisecond,
		LastDrainDuration:  500 * time.Millisecond,
		MaxDrainDuration:   1200 * time.Millisecond,
	}}
	collector, err := NewOutboundCollector(source, RuntimeCollectorConfig{
		Name:      "primary",
		Component: "tcp",
	})
	if err != nil {
		t.Fatal(err)
	}
	registry := prometheus.NewRegistry()
	if err := registry.Register(collector); err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	promhttp.HandlerFor(registry, promhttp.HandlerOpts{}).ServeHTTP(
		recorder,
		httptest.NewRequest("GET", "/metrics", nil),
	)
	body := recorder.Body.String()
	for _, want := range []string{
		`kpro_outbound_sessions{name="primary",transport="tcp"} 3`,
		`kpro_outbound_pending_writes{name="primary",transport="tcp"} 4`,
		`kpro_outbound_pending_bytes{name="primary",transport="tcp"} 512`,
		`kpro_outbound_waiting_writers{name="primary",transport="tcp"} 2`,
		`kpro_outbound_saturated_sessions{name="primary",transport="tcp"} 1`,
		`kpro_outbound_accepted_writes_total{name="primary",transport="tcp"} 20`,
		`kpro_outbound_accepted_bytes_total{name="primary",transport="tcp"} 4096`,
		`kpro_outbound_write_results_total{name="primary",result="success",transport="tcp"} 15`,
		`kpro_outbound_write_results_total{name="primary",result="error",transport="tcp"} 2`,
		`kpro_outbound_write_results_total{name="primary",result="dropped",transport="tcp"} 1`,
		`kpro_outbound_write_result_bytes_total{name="primary",result="dropped",transport="tcp"} 128`,
		`kpro_outbound_drains_total{name="primary",result="deadline",transport="tcp"} 1`,
		`kpro_outbound_drain_duration_seconds_total{name="primary",transport="tcp"} 2.5`,
		`kpro_outbound_last_drain_duration_seconds{name="primary",transport="tcp"} 0.5`,
		`kpro_outbound_max_drain_duration_seconds{name="primary",transport="tcp"} 1.2`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics output missing %q:\n%s", want, body)
		}
	}
}

func TestOutboundCollectorValidatesSourceAndIdentity(t *testing.T) {
	if _, err := NewOutboundCollector(nil, RuntimeCollectorConfig{Name: "main", Component: "tcp"}); err == nil {
		t.Fatal("nil source validation error = nil")
	}
	source := &outboundSnapshotSource{}
	for _, config := range []RuntimeCollectorConfig{
		{Component: "tcp"},
		{Name: "main"},
	} {
		if _, err := NewOutboundCollector(source, config); err == nil {
			t.Fatalf("config %+v validation error = nil", config)
		}
	}
}
