// Package facts defines immutable business facts and their transport envelope.
package facts

import "time"

// Fact is something that already happened in the domain.
type Fact interface {
	FactType() string
	AggregateID() string
}

// Envelope carries a fact through outbox and projection infrastructure.
type Envelope struct {
	ID            string
	Type          string
	AggregateID   string
	TenantID      string
	ActorID       string
	TraceID       string
	SchemaVersion int
	OccurredAt    time.Time
	Payload       []byte
}

// NewEnvelope builds a transport envelope for an encoded fact payload.
func NewEnvelope(id string, fact Fact, payload []byte, occurredAt time.Time) Envelope {
	version := 1
	if occurredAt.IsZero() {
		occurredAt = time.Now()
	}
	return Envelope{
		ID:            id,
		Type:          fact.FactType(),
		AggregateID:   fact.AggregateID(),
		SchemaVersion: version,
		OccurredAt:    occurredAt,
		Payload:       payload,
	}
}
