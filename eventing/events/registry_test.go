package events

import (
	"testing"
	"time"
)

type testEvent struct {
	ID string `json:"id"`
}

func (f testEvent) EventType() string   { return "test.event" }
func (f testEvent) AggregateID() string { return f.ID }

func TestRegistryDecodesEnvelope(t *testing.T) {
	codec := JSONCodec{}
	payload, err := codec.Encode(testEvent{ID: "u1"})
	if err != nil {
		t.Fatal(err)
	}
	envelope := NewEnvelope("f1", testEvent{ID: "u1"}, payload, time.Time{})

	registry := NewRegistry()
	if err := registry.Register("test.event", func(data []byte) (any, error) {
		var event testEvent
		err := codec.Decode(data, &event)
		return event, err
	}); err != nil {
		t.Fatal(err)
	}

	decoded, err := registry.Decode(envelope)
	if err != nil {
		t.Fatal(err)
	}
	event, ok := decoded.(testEvent)
	if !ok {
		t.Fatalf("expected testEvent, got %T", decoded)
	}
	if event.ID != "u1" {
		t.Fatalf("expected aggregate id u1, got %q", event.ID)
	}
}
