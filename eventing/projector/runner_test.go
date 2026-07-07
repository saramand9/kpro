package projector

import (
	"context"
	"errors"
	"testing"

	"github.com/saramand9/bignet/kpro/eventing/delivery"
	"github.com/saramand9/bignet/kpro/eventing/events"
)

type fakeDeliveryStore struct {
	records []delivery.Record
	acked   []string
	failed  []string
}

func (s *fakeDeliveryStore) Append(context.Context, ...events.Envelope) error {
	return nil
}

func (s *fakeDeliveryStore) Fetch(_ context.Context, consumer string, _ int) ([]delivery.Record, error) {
	for i := range s.records {
		s.records[i].Consumer = consumer
	}
	return s.records, nil
}

func (s *fakeDeliveryStore) Ack(_ context.Context, _ string, eventID string) error {
	s.acked = append(s.acked, eventID)
	return nil
}

func (s *fakeDeliveryStore) Fail(_ context.Context, _ string, eventID string, _ error) error {
	s.failed = append(s.failed, eventID)
	return nil
}

type testProjection struct {
	name string
	err  error
}

func (p testProjection) Name() string { return p.name }

func (p testProjection) Handle(context.Context, events.Envelope) error {
	return p.err
}

func TestRunnerAcksHandledEvents(t *testing.T) {
	store := &fakeDeliveryStore{
		records: []delivery.Record{{Envelope: events.Envelope{ID: "f1"}}},
	}
	runner := NewRunner(store, testProjection{name: "cache"})

	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.acked) != 1 || store.acked[0] != "f1" {
		t.Fatalf("expected f1 acked, got %#v", store.acked)
	}
	if len(store.failed) != 0 {
		t.Fatalf("expected no failed events, got %#v", store.failed)
	}
}

func TestRunnerFailsUnhandledEvents(t *testing.T) {
	store := &fakeDeliveryStore{
		records: []delivery.Record{{Envelope: events.Envelope{ID: "f1"}}},
	}
	runner := NewRunner(store, testProjection{name: "cache", err: errors.New("boom")})

	if err := runner.RunOnce(context.Background()); err == nil {
		t.Fatal("expected runner error")
	}
	if len(store.failed) != 1 || store.failed[0] != "f1" {
		t.Fatalf("expected f1 failed, got %#v", store.failed)
	}
}
