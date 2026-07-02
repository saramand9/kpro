// File: examples/https-modular/server/internal/app/routes.go
// Purpose: Provides runnable KPro example code.

package app

import (
	"github.com/saramand9/bignet/kpro/examples/https-modular/server/internal/modules/echo"
	"github.com/saramand9/bignet/kpro/examples/https-modular/server/internal/modules/health"
	"github.com/saramand9/bignet/kpro/examples/https-modular/server/internal/modules/users"
	"github.com/saramand9/bignet/kpro/server"
	"github.com/saramand9/bignet/kpro/transport/httpx"
)

type Config struct {
	Addr     string
	CertFile string
	KeyFile  string
}

type HTTPModule interface {
	RegisterHTTP(*httpx.Endpoint)
}

func RegisterRoutes(app *server.Server, cfg Config) {
	endpoint := app.HTTPS(cfg.Addr, httpx.Config{
		CertFile: cfg.CertFile,
		KeyFile:  cfg.KeyFile,
	})
	registerHTTP(endpoint,
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
