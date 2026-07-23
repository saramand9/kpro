// Package delivery defines reliable event delivery contracts.
package delivery

import (
	"context"
	"errors"
	"time"

	"github.com/saramand9/bignet/kpro/eventing/events"
)

// ErrLeaseLost means a worker tried to settle a delivery after its lease was
// reclaimed, expired, or otherwise ceased to identify the current attempt.
var ErrLeaseLost = errors.New("delivery lease lost")

// ErrNotDead means a replay was requested for a delivery that is not
// currently dead. Inflight deliveries are deliberately covered by this error
// so an active lease can never be replayed.
var ErrNotDead = errors.New("delivery is not dead")

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
	Envelope events.Envelope
	Consumer string
	Status   Status
	Attempts int
	// AvailableAt is the next time this delivery may be fetched. For an
	// inflight record it is the current lease expiry.
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

// AttemptStore optionally adds fenced settlement to Store. Attempts returned
// by Fetch act as receipts so a stale worker cannot settle a newer delivery.
// Projector runners prefer these methods when the store implements them.
type AttemptStore interface {
	Store
	AckAttempt(ctx context.Context, consumer string, eventID string, attempt int) error
	FailAttempt(ctx context.Context, consumer string, eventID string, attempt int, err error) error
}

// RenewableAttemptStore extends fenced settlement with lease renewal. The
// returned duration is measured locally by the caller, so renewal scheduling
// does not depend on worker and database wall clocks being synchronized.
type RenewableAttemptStore interface {
	AttemptStore
	RenewAttempt(ctx context.Context, consumer string, eventID string, attempt int) (time.Duration, error)
}

// LeaseAttempt identifies one fenced delivery lease.
type LeaseAttempt struct {
	Consumer string
	EventID  string
	Attempt  int
}

// LeaseRenewal is the outcome for one item passed to RenewAttempts. Results
// retain input ordering. Lost is true when the fenced attempt no longer owns a
// live lease; Remaining is positive only for successfully renewed leases.
type LeaseRenewal struct {
	LeaseAttempt
	Remaining time.Duration
	Lost      bool
}

// BatchRenewableAttemptStore optionally renews several fenced leases using one
// database-clock sample. A returned error means the batch could not be
// completed; per-item lease loss is reported through LeaseRenewal.Lost.
type BatchRenewableAttemptStore interface {
	RenewableAttemptStore
	RenewAttempts(ctx context.Context, attempts []LeaseAttempt) ([]LeaseRenewal, error)
}

// DeadLetter contains a dead delivery and its replay audit metadata. Record
// contains the current dead attempt and error. LastDeadAttempts and
// LastDeadError contain the snapshot preserved by the most recent replay.
type DeadLetter struct {
	Record           Record
	ReplayCount      int
	LastDeadAttempts int
	LastDeadError    string
	LastReplayedAt   *time.Time
}

// DeadLetterStore optionally supports operational dead-letter inspection and
// explicit replay.
type DeadLetterStore interface {
	Store
	DeadLetters(ctx context.Context, consumer string, limit int) ([]DeadLetter, error)
	ReplayDeadLetter(ctx context.Context, consumer string, eventID string) error
}

// BacklogStats reports current delivery counts for one consumer. Events with
// no materialized delivery row yet are counted as pending.
type BacklogStats struct {
	Pending  int64
	Inflight int64
	Failed   int64
	Dead     int64
}

// BacklogStore optionally exposes delivery backlog statistics.
type BacklogStore interface {
	Store
	Backlog(ctx context.Context, consumer string) (BacklogStats, error)
}
