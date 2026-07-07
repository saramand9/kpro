package projections

import (
	"context"
	"errors"
	"testing"

	"github.com/saramand9/bignet/kpro/database/facts"
	"github.com/saramand9/bignet/kpro/database/outbox"
)

type fakeOutbox struct {
	records []outbox.Record
	acked   []string
	failed  []string
}

func (s *fakeOutbox) Append(context.Context, ...facts.Envelope) error {
	return nil
}

func (s *fakeOutbox) Fetch(_ context.Context, consumer string, _ int) ([]outbox.Record, error) {
	for i := range s.records {
		s.records[i].Consumer = consumer
	}
	return s.records, nil
}

func (s *fakeOutbox) Ack(_ context.Context, _ string, factID string) error {
	s.acked = append(s.acked, factID)
	return nil
}

func (s *fakeOutbox) Fail(_ context.Context, _ string, factID string, _ error) error {
	s.failed = append(s.failed, factID)
	return nil
}

type testProjection struct {
	name string
	err  error
}

func (p testProjection) Name() string { return p.name }

func (p testProjection) Handle(context.Context, facts.Envelope) error {
	return p.err
}

func TestRunnerAcksHandledFacts(t *testing.T) {
	store := &fakeOutbox{
		records: []outbox.Record{{Envelope: facts.Envelope{ID: "f1"}}},
	}
	runner := NewRunner(store, testProjection{name: "cache"})

	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.acked) != 1 || store.acked[0] != "f1" {
		t.Fatalf("expected f1 acked, got %#v", store.acked)
	}
	if len(store.failed) != 0 {
		t.Fatalf("expected no failed facts, got %#v", store.failed)
	}
}

func TestRunnerFailsUnhandledFacts(t *testing.T) {
	store := &fakeOutbox{
		records: []outbox.Record{{Envelope: facts.Envelope{ID: "f1"}}},
	}
	runner := NewRunner(store, testProjection{name: "cache", err: errors.New("boom")})

	if err := runner.RunOnce(context.Background()); err == nil {
		t.Fatal("expected runner error")
	}
	if len(store.failed) != 1 || store.failed[0] != "f1" {
		t.Fatalf("expected f1 failed, got %#v", store.failed)
	}
}
