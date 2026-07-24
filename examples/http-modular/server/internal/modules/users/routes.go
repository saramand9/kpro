// File: examples/http-modular/server/internal/modules/users/routes.go
// Purpose: Provides runnable KPro example code.

package users

import "github.com/saramand9/kpro/transport/httpx"

type Module struct{}

func (Module) RegisterHTTP(e *httpx.Endpoint) {
	e.Get("/users", list)
	e.Get("/users/active", active)
}
