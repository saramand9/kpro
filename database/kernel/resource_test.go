package kernel

import (
	"context"
	"errors"
	"testing"
)

type testResource struct {
	name string
	kind Kind
	err  error
}

func (r testResource) Name() string { return r.name }
func (r testResource) Kind() Kind   { return r.kind }
func (r testResource) Ping(context.Context) error {
	return r.err
}
func (r testResource) Health(ctx context.Context) Health {
	return CheckHealth(ctx, r)
}
func (r testResource) Close() error {
	return r.err
}

func TestResourceSetHealth(t *testing.T) {
	resources := NewResourceSet(
		testResource{name: "main", kind: KindSQL},
		testResource{name: "search", kind: KindSearch, err: errors.New("down")},
	)

	report := resources.Health(context.Background())
	if len(report) != 2 {
		t.Fatalf("expected 2 health entries, got %d", len(report))
	}
	if report[0].Status != StatusOK {
		t.Fatalf("expected first resource ok, got %s", report[0].Status)
	}
	if report[1].Status != StatusDown {
		t.Fatalf("expected second resource down, got %s", report[1].Status)
	}
}
