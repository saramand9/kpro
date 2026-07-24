// File: examples/tcp-modular/server/internal/app/routes.go
// Purpose: Provides runnable KPro example code.

package app

import (
	"github.com/saramand9/kpro/codec"
	"github.com/saramand9/kpro/examples/tcp-modular/server/internal/modules/echo"
	"github.com/saramand9/kpro/middleware"
	"github.com/saramand9/kpro/server"
	"github.com/saramand9/kpro/transport/tcp"
)

type Config struct {
	Addr string
}

type MessageModule interface {
	RegisterMessage(*server.Endpoint)
}

func RegisterRoutes(app *server.Server, cfg Config) {
	endpoint := app.Mount(tcp.New(tcp.Config{
		Addr:         cfg.Addr,
		Codec:        codec.KProV1(),
		MaxFrameSize: 64 * 1024,
	})).Use(middleware.Recover(), middleware.MaxPayload(64*1024))

	registerMessages(endpoint, echo.Module{})
}

func registerMessages(endpoint *server.Endpoint, modules ...MessageModule) {
	for _, module := range modules {
		module.RegisterMessage(endpoint)
	}
}
