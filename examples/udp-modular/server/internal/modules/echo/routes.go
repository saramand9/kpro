// File: examples/udp-modular/server/internal/modules/echo/routes.go
// Purpose: Provides runnable KPro example code.

package echo

import (
	"github.com/saramand9/bignet/kpro/examples/internal/msgid"
	"github.com/saramand9/bignet/kpro/server"
)

type Module struct{}

func (Module) RegisterMessage(e *server.Endpoint) {
	e.HandleSync(msgid.Echo, echo)
}
