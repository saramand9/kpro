package projector

import (
	"context"
	"errors"
	"fmt"

	"github.com/saramand9/bignet/kpro/eventing/delivery"
)

// Runner pulls events from an delivery store and applies registered projector.
type Runner struct {
	store      delivery.Store
	projectors []Projector
	batchSize  int
}

// NewRunner creates a projector runner.
func NewRunner(store delivery.Store, projectors ...Projector) *Runner {
	return &Runner{
		store:      store,
		projectors: append([]Projector(nil), projectors...),
		batchSize:  100,
	}
}

// WithBatchSize sets the max records fetched per projector pass.
func (r *Runner) WithBatchSize(batchSize int) *Runner {
	if batchSize > 0 {
		r.batchSize = batchSize
	}
	return r
}

// RunOnce processes one batch for each projector.
func (r *Runner) RunOnce(ctx context.Context) error {
	if r == nil || r.store == nil {
		return fmt.Errorf("projector runner requires an delivery store")
	}
	var errs []error
	for _, projector := range r.projectors {
		if projector == nil {
			continue
		}
		records, err := r.store.Fetch(ctx, projector.Name(), r.batchSize)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, record := range records {
			if err := projector.Handle(ctx, record.Envelope); err != nil {
				errs = append(errs, err)
				errs = append(errs, r.store.Fail(ctx, projector.Name(), record.Envelope.ID, err))
				continue
			}
			if err := r.store.Ack(ctx, projector.Name(), record.Envelope.ID); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}
