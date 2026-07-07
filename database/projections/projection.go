// Package projections runs facts into query views.
package projections

import (
	"context"

	"github.com/saramand9/bignet/kpro/database/facts"
)

// Projection handles one fact stream and updates one or more views.
type Projection interface {
	Name() string
	Handle(ctx context.Context, envelope facts.Envelope) error
}
