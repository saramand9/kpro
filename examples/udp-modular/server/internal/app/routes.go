// File: examples/udp-modular/server/internal/app/routes.go
// Purpose: Provides runnable KPro example code.

package app

import (
	"github.com/saramand9/bignet/kpro/codec"
	"github.com/saramand9/bignet/kpro/examples/udp-modular/server/internal/modules/echo"
	"github.com/saramand9/bignet/kpro/middleware"
	"github.com/saramand9/bignet/kpro/server"
	"github.com/saramand9/bignet/kpro/transport/udp"
)

type Config struct {
	Addr string
}

type MessageModule interface {
	RegisterMessage(*server.Endpoint)
}

func RegisterRoutes(app *server.Server, cfg Config) {
	endpoint := app.UDP(cfg.Addr, udp.Config{
		Codec:         codec.KProV1(),
		MaxPacketSize: 2048,
	}).Use(middleware.Recover(), middleware.MaxPayload(1500))

	registerMessages(endpoint, echo.Module{})
}

func registerMessages(endpoint *server.Endpoint, modules ...MessageModule) {
	for _, module := range modules {
		module.RegisterMessage(endpoint)
	}
}
