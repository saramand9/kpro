// package projector runs events into read models.
package projector

import (
	"context"

	"github.com/saramand9/bignet/kpro/eventing/events"
)

// Projector handles one event stream and updates one or more read models.
type Projector interface {
	Name() string
	Handle(ctx context.Context, envelope events.Envelope) error
}
