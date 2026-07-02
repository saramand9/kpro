// File: examples/modular/server/internal/modules/users/routes.go
// Purpose: Provides runnable KPro example code.

package users

import "github.com/saramand9/bignet/kpro/transport/httpx"

type Module struct{}

func (Module) RegisterHTTP(e *httpx.Endpoint) {
	e.Get("/users", list)
	e.Get("/users/active", active)
}
