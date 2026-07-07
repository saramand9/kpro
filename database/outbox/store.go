// Package outbox defines reliable fact delivery contracts.
package outbox

import (
	"context"
	"time"

	"github.com/saramand9/bignet/kpro/database/facts"
)

// Status is a fact delivery state for one projection consumer.
type Status string

const (
	StatusPending   Status = "pending"
	StatusInflight  Status = "inflight"
	StatusDelivered Status = "delivered"
	StatusFailed    Status = "failed"
	StatusDead      Status = "dead"
)

// Record is one fact ready for projection delivery.
type Record struct {
	Envelope    facts.Envelope
	Consumer    string
	Status      Status
	Attempts    int
	AvailableAt time.Time
	LastError   string
}

// Store persists facts and per-consumer delivery state.
type Store interface {
	Append(ctx context.Context, envelopes ...facts.Envelope) error
	Fetch(ctx context.Context, consumer string, limit int) ([]Record, error)
	Ack(ctx context.Context, consumer string, factID string) error
	Fail(ctx context.Context, consumer string, factID string, err error) error
}
