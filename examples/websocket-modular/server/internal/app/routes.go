// File: examples/websocket-modular/server/internal/app/routes.go
// Purpose: Provides runnable KPro example code.

package app

import (
	"github.com/saramand9/kpro/codec"
	"github.com/saramand9/kpro/examples/websocket-modular/server/internal/modules/echo"
	"github.com/saramand9/kpro/middleware"
	"github.com/saramand9/kpro/server"
	"github.com/saramand9/kpro/transport/websocket"
)

type Config struct {
	Addr string
	Path string
}

type MessageModule interface {
	RegisterMessage(*server.Endpoint)
}

func RegisterRoutes(app *server.Server, cfg Config) {
	endpoint := app.Mount(websocket.New(websocket.Config{
		Addr:       cfg.Addr,
		Path:       cfg.Path,
		Codec:      codec.KProV1(),
		MaxMessage: 64 * 1024,
	})).Use(middleware.Recover(), middleware.MaxPayload(64*1024))

	registerMessages(endpoint, echo.Module{})
}

func registerMessages(endpoint *server.Endpoint, modules ...MessageModule) {
	for _, module := range modules {
		module.RegisterMessage(endpoint)
	}
}
