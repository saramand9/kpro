package projections

import (
	"context"
	"errors"
	"fmt"

	"github.com/saramand9/bignet/kpro/database/outbox"
)

// Runner pulls facts from an outbox store and applies registered projections.
type Runner struct {
	store       outbox.Store
	projections []Projection
	batchSize   int
}

// NewRunner creates a projection runner.
func NewRunner(store outbox.Store, projections ...Projection) *Runner {
	return &Runner{
		store:       store,
		projections: append([]Projection(nil), projections...),
		batchSize:   100,
	}
}

// WithBatchSize sets the max records fetched per projection pass.
func (r *Runner) WithBatchSize(batchSize int) *Runner {
	if batchSize > 0 {
		r.batchSize = batchSize
	}
	return r
}

// RunOnce processes one batch for each projection.
func (r *Runner) RunOnce(ctx context.Context) error {
	if r == nil || r.store == nil {
		return fmt.Errorf("projection runner requires an outbox store")
	}
	var errs []error
	for _, projection := range r.projections {
		if projection == nil {
			continue
		}
		records, err := r.store.Fetch(ctx, projection.Name(), r.batchSize)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, record := range records {
			if err := projection.Handle(ctx, record.Envelope); err != nil {
				errs = append(errs, err)
				errs = append(errs, r.store.Fail(ctx, projection.Name(), record.Envelope.ID, err))
				continue
			}
			if err := r.store.Ack(ctx, projection.Name(), record.Envelope.ID); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}
