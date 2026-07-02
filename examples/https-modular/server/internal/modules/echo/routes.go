// File: examples/https-modular/server/internal/modules/echo/routes.go
// Purpose: Provides runnable KPro example code.

package echo

import "github.com/saramand9/bignet/kpro/transport/httpx"

type Module struct{}

func (Module) RegisterHTTP(e *httpx.Endpoint) {
	e.Post("/echo", echo)
}
