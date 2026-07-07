// Package delivery defines reliable event delivery contracts.
package delivery

import (
	"context"
	"time"

	"github.com/saramand9/bignet/kpro/eventing/events"
)

// Status is an event delivery state for one projector consumer.
type Status string

const (
	StatusPending   Status = "pending"
	StatusInflight  Status = "inflight"
	StatusDelivered Status = "delivered"
	StatusFailed    Status = "failed"
	StatusDead      Status = "dead"
)

// Record is one event ready for projector delivery.
type Record struct {
	Envelope    events.Envelope
	Consumer    string
	Status      Status
	Attempts    int
	AvailableAt time.Time
	LastError   string
}

// Store persists events and per-consumer delivery state.
type Store interface {
	Append(ctx context.Context, envelopes ...events.Envelope) error
	Fetch(ctx context.Context, consumer string, limit int) ([]Record, error)
	Ack(ctx context.Context, consumer string, eventID string) error
	Fail(ctx context.Context, consumer string, eventID string, err error) error
}
