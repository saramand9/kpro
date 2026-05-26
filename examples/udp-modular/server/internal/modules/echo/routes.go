package echo

import (
	"github.com/saramand9/bignet/kpro/core"
	"github.com/saramand9/bignet/kpro/server"
)

type Module struct{}

func (Module) RegisterMessage(e *server.Endpoint) {
	e.HandleSync(core.MsgEcho, echo)
}
