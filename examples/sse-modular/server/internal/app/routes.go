package app

import (
	"github.com/saramand9/bignet/kpro/examples/sse-modular/server/internal/modules/events"
	"github.com/saramand9/bignet/kpro/server"
	"github.com/saramand9/bignet/kpro/transport/sse"
)

type Config struct {
	Addr string
	Path string
}

type SSEModule interface {
	RegisterSSE(*sse.Endpoint)
}

func RegisterRoutes(app *server.Server, cfg Config) {
	endpoint := app.SSE(cfg.Addr, cfg.Path)
	registerSSE(endpoint, events.Module{})
}

func registerSSE(endpoint *sse.Endpoint, modules ...SSEModule) {
	for _, module := range modules {
		module.RegisterSSE(endpoint)
	}
}
