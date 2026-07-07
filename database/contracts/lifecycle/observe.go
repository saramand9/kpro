package lifecycle

import (
	"context"
	"time"
)

// Observer receives coarse data-layer operation events.
type Observer interface {
	Observe(ctx context.Context, event Event)
}

// Event describes one data-layer operation for logging or metrics adapters.
type Event struct {
	Resource string
	Kind     Kind
	Op       string
	Started  time.Time
	Elapsed  time.Duration
	Err      error
}

// NoopObserver drops all events.
type NoopObserver struct{}

// Observe implements Observer.
func (NoopObserver) Observe(context.Context, Event) {}
