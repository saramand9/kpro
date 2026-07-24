package prometheusx

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/saramand9/kpro/core"
)

func TestRuntimeCollectorsExposeLifecycleAndBackpressure(t *testing.T) {
	registry := prometheus.NewRegistry()
	lifecycle, err := NewLifecycleCollector(fakeLifecycle{state: core.StateRunning}, RuntimeCollectorConfig{
		Component: "server",
		Name:      "orders",
	})
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewRouterCollector(fakeRouterSnapshotter{stats: core.RouterStats{
		State:           core.StateRunning,
		Inflight:        3,
		MaxInflight:     100,
		Sessions:        2,
		SyncQueueDepth:  1,
		AsyncQueueDepth: 4,
		Accepted:        20,
		Completed:       17,
		Rejected:        2,
		Waited:          5,
	}}, RuntimeCollectorConfig{Name: "tcp"})
	if err != nil {
		t.Fatal(err)
	}
	registry.MustRegister(lifecycle, router)
	secondLifecycle, err := NewLifecycleCollector(fakeLifecycle{state: core.StateCreated}, RuntimeCollectorConfig{
		Component: "server",
		Name:      "billing",
	})
	if err != nil {
		t.Fatal(err)
	}
	secondRouter, err := NewRouterCollector(fakeRouterSnapshotter{}, RuntimeCollectorConfig{Name: "udp"})
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(secondLifecycle); err != nil {
		t.Fatalf("register second lifecycle collector: %v", err)
	}
	if err := registry.Register(secondRouter); err != nil {
		t.Fatalf("register second router collector: %v", err)
	}
	recorder := httptest.NewRecorder()
	promhttp.HandlerFor(registry, promhttp.HandlerOpts{}).ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	body := recorder.Body.String()
	wanted := []string{
		`kpro_lifecycle_state{component="server",name="orders",state="running"} 1`,
		`kpro_router_inflight{kind="current",router="tcp"} 3`,
		`kpro_router_inflight{kind="limit",router="tcp"} 100`,
		`kpro_router_dispatch_total{outcome="rejected",router="tcp"} 2`,
	}
	for _, value := range wanted {
		if !strings.Contains(body, value) {
			t.Fatalf("metrics output missing %q:\n%s", value, body)
		}
	}
}

type fakeLifecycle struct{ state core.LifecycleState }

func (f fakeLifecycle) State() core.LifecycleState { return f.state }

type fakeRouterSnapshotter struct{ stats core.RouterStats }

func (f fakeRouterSnapshotter) Snapshot() core.RouterStats { return f.stats }
