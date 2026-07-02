// File: appx/module.go
// Purpose: Defines lightweight application module contracts.

package appx

import (
	"context"

	"github.com/saramand9/bignet/kpro/server"
	"github.com/saramand9/bignet/kpro/transport/httpx"
)

// Module registers application routes, handlers, and resources with a Server.
type Module interface {
	Register(context.Context, *server.Server) error
}

// HTTPModule registers routes on one HTTP endpoint.
type HTTPModule interface {
	RegisterHTTP(*httpx.Endpoint)
}

// Register installs modules on app in order.
func Register(ctx context.Context, app *server.Server, modules ...Module) error {
	if ctx == nil {
		ctx = context.Background()
	}
	for _, module := range modules {
		if module == nil {
			continue
		}
		if err := module.Register(ctx, app); err != nil {
			return err
		}
	}
	return nil
}

// RegisterHTTP installs HTTP modules on endpoint in order.
func RegisterHTTP(endpoint *httpx.Endpoint, modules ...HTTPModule) {
	for _, module := range modules {
		if module == nil {
			continue
		}
		module.RegisterHTTP(endpoint)
	}
}
