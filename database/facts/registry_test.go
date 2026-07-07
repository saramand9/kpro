package facts

import (
	"testing"
	"time"
)

type testFact struct {
	ID string `json:"id"`
}

func (f testFact) FactType() string    { return "test.fact" }
func (f testFact) AggregateID() string { return f.ID }

func TestRegistryDecodesEnvelope(t *testing.T) {
	codec := JSONCodec{}
	payload, err := codec.Encode(testFact{ID: "u1"})
	if err != nil {
		t.Fatal(err)
	}
	envelope := NewEnvelope("f1", testFact{ID: "u1"}, payload, time.Time{})

	registry := NewRegistry()
	if err := registry.Register("test.fact", func(data []byte) (any, error) {
		var fact testFact
		err := codec.Decode(data, &fact)
		return fact, err
	}); err != nil {
		t.Fatal(err)
	}

	decoded, err := registry.Decode(envelope)
	if err != nil {
		t.Fatal(err)
	}
	fact, ok := decoded.(testFact)
	if !ok {
		t.Fatalf("expected testFact, got %T", decoded)
	}
	if fact.ID != "u1" {
		t.Fatalf("expected aggregate id u1, got %q", fact.ID)
	}
}
