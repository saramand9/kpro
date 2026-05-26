package echo

import (
	"github.com/saramand9/bignet/kpro/core"
	"github.com/saramand9/bignet/kpro/server"
	"github.com/saramand9/bignet/kpro/transport/httpx"
)

type Module struct{}

func (Module) RegisterHTTP(e *httpx.Endpoint) {
	e.Post("/echo", httpEcho)
}

func (Module) RegisterMessage(e *server.Endpoint) {
	e.HandleSync(core.MsgEcho, messageEcho)
}
