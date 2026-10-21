package outbound

import (
	"context"
	"errors"
)

// DrainQueues seals the complete snapshot before waiting for any queue. An
// earlier slow writer must not leave later queues accepting new writes.
// It attempts every queue and preserves both write and context errors.
func DrainQueues[T any](ctx context.Context, queues []*Queue[T]) error {
	if ctx == nil {
		ctx = context.Background()
	}
	for _, queue := range queues {
		queue.Seal()
	}
	var drainErr error
	for _, queue := range queues {
		drainErr = errors.Join(drainErr, queue.WaitDrained(ctx))
	}
	return drainErr
}
