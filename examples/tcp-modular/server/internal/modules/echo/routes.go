// File: examples/tcp-modular/server/internal/modules/echo/routes.go
// Purpose: Provides runnable KPro example code.

package echo

import (
	"github.com/saramand9/kpro/examples/internal/msgid"
	"github.com/saramand9/kpro/server"
)

type Module struct{}

func (Module) RegisterMessage(e *server.Endpoint) {
	e.HandleSync(msgid.Echo, echo)
}
