// Package sessionoutbound maps queue errors to the public session flow-control
// contract. Queue accounting remains independent of core.
package sessionoutbound

import (
	"errors"

	"github.com/saramand9/kpro/core"
	"github.com/saramand9/kpro/transport/internal/outbound"
)

// MapWaitError retains the session error contract for internal queue waits.
func MapWaitError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, outbound.ErrClosed):
		return core.ErrClosed
	case errors.Is(err, outbound.ErrTooManyWaiters):
		return core.ErrTooManyOutboundWaiters
	default:
		return err
	}
}
