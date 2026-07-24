// File: examples/modular/server/internal/modules/events/routes.go
// Purpose: Provides runnable KPro example code.

package events

import "github.com/saramand9/kpro/transport/sse"

type Module struct{}

func (Module) RegisterSSE(e *sse.Endpoint) {
	e.Handle(timeStream)
}
