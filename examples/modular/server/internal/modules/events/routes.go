package events

import "github.com/saramand9/bignet/kpro/transport/sse"

type Module struct{}

func (Module) RegisterSSE(e *sse.Endpoint) {
	e.Handle(timeStream)
}
