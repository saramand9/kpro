package prometheusx

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/saramand9/bignet/kpro/eventing/delivery"
	"github.com/saramand9/bignet/kpro/eventing/projector"
)

type fakeBacklogSource struct {
	mu      sync.Mutex
	stats   map[string]delivery.BacklogStats
	errs    map[string]error
	blocked map[string]bool
	calls   map[string]int
}

func (s *fakeBacklogSource) Backlog(ctx context.Context, consumer string) (delivery.BacklogStats, error) {
	s.mu.Lock()
	s.calls[consumer]++
	blocked := s.blocked[consumer]
	stats := s.stats[consumer]
	err := s.errs[consumer]
	s.mu.Unlock()
	if blocked {
		<-ctx.Done()
		return delivery.BacklogStats{}, context.Cause(ctx)
	}
	return stats, err
}

func (s *fakeBacklogSource) callCount(consumer string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[consumer]
}

type fakeProjectorSource struct {
	snapshot projector.Snapshot
	health   error
}

func (s fakeProjectorSource) Snapshot() projector.Snapshot { return s.snapshot }
func (s fakeProjectorSource) Health() error                { return s.health }

func TestBacklogCollectorExportsExplicitConsumersAndScrapeSuccess(t *testing.T) {
	source := &fakeBacklogSource{
		stats: map[string]delivery.BacklogStats{
			"orders":       {Pending: 3, Inflight: 2, Failed: 1, Dead: 4},
			"not-exported": {Pending: 99},
		},
		errs: map[string]error{
			"failing": errors.New("tenant-12345 database failed"),
		},
		blocked: map[string]bool{"slow": true},
		calls:   make(map[string]int),
	}
	collector, err := NewBacklogCollector(
		source,
		[]string{"slow", "orders", "failing"},
		EventingCollectorConfig{Timeout: 20 * time.Millisecond},
	)
	if err != nil {
		t.Fatal(err)
	}
	registry := prometheus.NewRegistry()
	if err := registry.Register(collector); err != nil {
		t.Fatal(err)
	}

	started := time.Now()
	body := gatherMetrics(t, registry)
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("backlog scrape took %s, timeout was not enforced", elapsed)
	}
	for _, line := range []string{
		`kpro_eventing_delivery_backlog{consumer="orders",status="pending"} 3`,
		`kpro_eventing_delivery_backlog{consumer="orders",status="inflight"} 2`,
		`kpro_eventing_delivery_backlog{consumer="orders",status="failed"} 1`,
		`kpro_eventing_delivery_backlog{consumer="orders",status="dead"} 4`,
		`kpro_eventing_delivery_backlog_scrape_success{consumer="orders"} 1`,
		`kpro_eventing_delivery_backlog_scrape_success{consumer="failing"} 0`,
		`kpro_eventing_delivery_backlog_scrape_success{consumer="slow"} 0`,
	} {
		if !strings.Contains(body, line) {
			t.Fatalf("metrics output missing %q:\n%s", line, body)
		}
	}
	if strings.Contains(body, `consumer="not-exported"`) || strings.Contains(body, "tenant-12345") {
		t.Fatalf("metrics exposed a non-configured or error-derived label:\n%s", body)
	}
	if source.callCount("orders") != 1 || source.callCount("failing") != 1 || source.callCount("slow") != 1 || source.callCount("not-exported") != 0 {
		t.Fatalf("unexpected backlog calls: %#v", source.calls)
	}
}

func TestProjectorCollectorExportsSnapshotAndHealth(t *testing.T) {
	started := time.Unix(100, int64(500*time.Millisecond)).UTC()
	lastPass := time.Unix(200, 0).UTC()
	lastError := time.Unix(190, 0).UTC()
	collector, err := NewProjectorCollector(map[string]ProjectorSource{
		"search": fakeProjectorSource{
			snapshot: projector.Snapshot{
				Running:           true,
				StartedAt:         started,
				LastPassAt:        lastPass,
				LastErrorAt:       lastError,
				LastError:         "customer-928 failed",
				ConsecutiveErrors: 2,
				TotalPasses:       11,
				TotalProcessed:    37,
				TotalErrors:       3,
			},
			health: errors.New("unhealthy"),
		},
		"billing": fakeProjectorSource{
			snapshot: projector.Snapshot{
				LastSuccessAt:  time.Unix(180, 0).UTC(),
				StoppedAt:      time.Unix(210, 0).UTC(),
				TotalPasses:    7,
				TotalProcessed: 20,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	registry := prometheus.NewRegistry()
	if err := registry.Register(collector); err != nil {
		t.Fatal(err)
	}
	body := gatherMetrics(t, registry)

	for _, line := range []string{
		`kpro_eventing_projector_running{projector="search"} 1`,
		`kpro_eventing_projector_healthy{projector="search"} 0`,
		`kpro_eventing_projector_consecutive_errors{projector="search"} 2`,
		`kpro_eventing_projector_started_timestamp_seconds{projector="search"} 100.5`,
		`kpro_eventing_projector_last_pass_timestamp_seconds{projector="search"} 200`,
		`kpro_eventing_projector_last_error_timestamp_seconds{projector="search"} 190`,
		`kpro_eventing_projector_passes_total{projector="search"} 11`,
		`kpro_eventing_projector_processed_total{projector="search"} 37`,
		`kpro_eventing_projector_errors_total{projector="search"} 3`,
		`kpro_eventing_projector_running{projector="billing"} 0`,
		`kpro_eventing_projector_healthy{projector="billing"} 1`,
		`kpro_eventing_projector_stopped_timestamp_seconds{projector="billing"} 210`,
	} {
		if !strings.Contains(body, line) {
			t.Fatalf("metrics output missing %q:\n%s", line, body)
		}
	}
	if strings.Contains(body, "customer-928") {
		t.Fatalf("metrics exposed LastError as a label or value:\n%s", body)
	}
}

func TestEventingCollectorsRejectUnboundedTargetDefinitions(t *testing.T) {
	source := &fakeBacklogSource{calls: make(map[string]int)}
	if _, err := NewBacklogCollector(source, nil); err == nil {
		t.Fatal("expected missing consumer error")
	}
	if _, err := NewBacklogCollector(source, []string{"orders", "orders"}); err == nil {
		t.Fatal("expected duplicate consumer error")
	}
	if _, err := NewProjectorCollector(nil); err == nil {
		t.Fatal("expected missing projector source error")
	}
	if _, err := NewProjectorCollector(map[string]ProjectorSource{"": fakeProjectorSource{}}); err == nil {
		t.Fatal("expected empty projector name error")
	}
}

func TestBacklogCollectorDoesNotOverlapStuckSourceCalls(t *testing.T) {
	release := make(chan struct{})
	var calls atomic.Int32
	source := backlogSourceFunc(func(context.Context, string) (delivery.BacklogStats, error) {
		calls.Add(1)
		<-release
		return delivery.BacklogStats{}, nil
	})
	collector, err := NewBacklogCollector(source, []string{"stuck"}, EventingCollectorConfig{Timeout: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	registry := prometheus.NewRegistry()
	registry.MustRegister(collector)
	for index := 0; index < 10; index++ {
		body := gatherMetrics(t, registry)
		if !strings.Contains(body, `kpro_eventing_delivery_backlog_scrape_success{consumer="stuck"} 0`) {
			t.Fatalf("scrape %d output:\n%s", index, body)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("stuck source calls = %d, want 1", got)
	}
	close(release)
}

type backlogSourceFunc func(context.Context, string) (delivery.BacklogStats, error)

func (f backlogSourceFunc) Backlog(ctx context.Context, consumer string) (delivery.BacklogStats, error) {
	return f(ctx, consumer)
}

func gatherMetrics(t *testing.T, gatherer prometheus.Gatherer) string {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler := promhttp.HandlerFor(gatherer, promhttp.HandlerOpts{})
	handler.ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	if recorder.Code != 200 {
		t.Fatalf("metrics status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	return recorder.Body.String()
}
