package echo

import "github.com/saramand9/bignet/kpro/transport/httpx"

type Module struct{}

func (Module) RegisterHTTP(e *httpx.Endpoint) {
	e.Post("/echo", echo)
}
