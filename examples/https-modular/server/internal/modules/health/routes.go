// File: examples/https-modular/server/internal/modules/health/routes.go
// Purpose: Provides runnable KPro example code.

package health

import "github.com/saramand9/bignet/kpro/transport/httpx"

type Module struct{}

func (Module) RegisterHTTP(e *httpx.Endpoint) {
	e.Get("/health", health)
}
