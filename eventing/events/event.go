// package events defines immutable business events and their transport envelope.
package events

import "time"

// Event is something that already happened in the domain.
type Event interface {
	EventType() string
	AggregateID() string
}

// Envelope carries an event through delivery and projector infrastructure.
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

// NewEnvelope builds a transport envelope for an encoded event payload.
func NewEnvelope(id string, event Event, payload []byte, occurredAt time.Time) Envelope {
	version := 1
	if occurredAt.IsZero() {
		occurredAt = time.Now()
	}
	return Envelope{
		ID:            id,
		Type:          event.EventType(),
		AggregateID:   event.AggregateID(),
		SchemaVersion: version,
		OccurredAt:    occurredAt,
		Payload:       payload,
	}
}
