// File: examples/http-modular/server/internal/app/routes.go
// Purpose: Provides runnable KPro example code.

package app

import (
	"github.com/saramand9/kpro/examples/http-modular/server/internal/modules/echo"
	"github.com/saramand9/kpro/examples/http-modular/server/internal/modules/health"
	"github.com/saramand9/kpro/examples/http-modular/server/internal/modules/users"
	"github.com/saramand9/kpro/server"
	"github.com/saramand9/kpro/transport/httpx"
)

type Config struct {
	HTTPAddr string
}

type HTTPModule interface {
	RegisterHTTP(*httpx.Endpoint)
}

func RegisterRoutes(app *server.Server, cfg Config) {
	httpEndpoint := app.HTTP(cfg.HTTPAddr)

	registerHTTP(httpEndpoint,
		health.Module{},
		echo.Module{},
		users.Module{},
	)
}

func registerHTTP(endpoint *httpx.Endpoint, modules ...HTTPModule) {
	for _, module := range modules {
		module.RegisterHTTP(endpoint)
	}
}
