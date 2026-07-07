// Package lifecycle defines the shared lifecycle surface for data resources.
package lifecycle

import (
	"context"
	"errors"
	"time"
)

// Kind describes the natural storage capability of a resource.
type Kind string

const (
	KindSQL       Kind = "sql"
	KindDocument  Kind = "document"
	KindRedis     Kind = "redis"
	KindSearch    Kind = "search"
	KindVector    Kind = "vector"
	KindGraph     Kind = "graph"
	KindAnalytics Kind = "analytics"
	KindMetrics   Kind = "metrics"
)

// Status is the current health state of a data resource.
type Status string

const (
	StatusOK       Status = "ok"
	StatusDegraded Status = "degraded"
	StatusDown     Status = "down"
)

// Health reports one resource's observable state.
type Health struct {
	Name      string
	Kind      Kind
	Status    Status
	CheckedAt time.Time
	Error     string
	Details   map[string]any
}

// Resource is the minimum lifecycle contract shared by all data adapters.
type Resource interface {
	Name() string
	Kind() Kind
	Ping(ctx context.Context) error
	Health(ctx context.Context) Health
	Close() error
}

// CheckHealth runs Ping and builds a standard health result.
func CheckHealth(ctx context.Context, resource Resource) Health {
	if resource == nil {
		return Health{
			Status:    StatusDown,
			CheckedAt: time.Now(),
			Error:     "nil resource",
		}
	}
	health := Health{
		Name:      resource.Name(),
		Kind:      resource.Kind(),
		Status:    StatusOK,
		CheckedAt: time.Now(),
	}
	if err := resource.Ping(ctx); err != nil {
		health.Status = StatusDown
		health.Error = err.Error()
	}
	return health
}

// ResourceSet owns a small explicit list of resources for health and shutdown.
type ResourceSet struct {
	resources []Resource
}

// NewResourceSet creates a lifecycle group without adding name-based lookup.
func NewResourceSet(resources ...Resource) *ResourceSet {
	return &ResourceSet{resources: append([]Resource(nil), resources...)}
}

// Resources returns a copy of the managed resource list.
func (s *ResourceSet) Resources() []Resource {
	if s == nil {
		return nil
	}
	return append([]Resource(nil), s.resources...)
}

// Health checks every resource in order.
func (s *ResourceSet) Health(ctx context.Context) []Health {
	if s == nil {
		return nil
	}
	report := make([]Health, 0, len(s.resources))
	for _, resource := range s.resources {
		report = append(report, CheckHealth(ctx, resource))
	}
	return report
}

// Close closes every resource and joins close errors.
func (s *ResourceSet) Close() error {
	if s == nil {
		return nil
	}
	errs := make([]error, 0)
	for i := len(s.resources) - 1; i >= 0; i-- {
		if s.resources[i] == nil {
			continue
		}
		if err := s.resources[i].Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
