// File: examples/modular/server/internal/modules/echo/routes.go
// Purpose: Provides runnable KPro example code.

package echo

import (
	"github.com/saramand9/bignet/kpro/examples/internal/msgid"
	"github.com/saramand9/bignet/kpro/server"
	"github.com/saramand9/bignet/kpro/transport/httpx"
)

type Module struct{}

func (Module) RegisterHTTP(e *httpx.Endpoint) {
	e.Post("/echo", httpEcho)
}

func (Module) RegisterMessage(e *server.Endpoint) {
	e.HandleSync(msgid.Echo, messageEcho)
}
