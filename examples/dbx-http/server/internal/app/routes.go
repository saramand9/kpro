// File: examples/dbx-http/server/internal/app/routes.go
// Purpose: Provides runnable KPro database example code.

package app

import (
	"context"

	"github.com/saramand9/bignet/kpro/database/dbx"
	"github.com/saramand9/bignet/kpro/server"
)

// Config controls mounted routes for the dbx HTTP example.
type Config struct {
	HTTPAddr string
	UDPAddr  string
	GRPCAddr string
}

// RegisterRoutes wires application modules to protocol endpoints.
func RegisterRoutes(ctx context.Context, app *server.Server, database *dbx.Database, config Config) error {
	if err := InitDBXSchemas(ctx, database); err != nil {
		return err
	}

	httpEndpoint := app.HTTP(config.HTTPAddr)
	if err := RegisterHTTPModules(httpEndpoint, NewDBXStores(database)); err != nil {
		return err
	}
	MountOptionalProtocolModules(app, config)
	return nil
}
