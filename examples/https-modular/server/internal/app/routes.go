// File: examples/https-modular/server/internal/app/routes.go
// Purpose: Provides runnable KPro example code.

package app

import (
	"github.com/saramand9/kpro/examples/https-modular/server/internal/modules/echo"
	"github.com/saramand9/kpro/examples/https-modular/server/internal/modules/health"
	"github.com/saramand9/kpro/examples/https-modular/server/internal/modules/users"
	"github.com/saramand9/kpro/server"
	"github.com/saramand9/kpro/transport/httpx"
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
	endpoint := httpx.New(httpx.Config{
		Addr:     cfg.Addr,
		TLS:      true,
		CertFile: cfg.CertFile,
		KeyFile:  cfg.KeyFile,
	})
	app.MountRunner(endpoint)
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
