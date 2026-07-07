// File: examples/gorm-http/server/internal/app/routes.go
// Purpose: Provides runnable KPro database example code.

package app

import (
	"context"

	gormstore "github.com/saramand9/bignet/kpro/database/relational/gorm"
	"github.com/saramand9/bignet/kpro/server"
)

// Config controls mounted routes for the GORM HTTP example.
type Config struct {
	HTTPAddr string
	UDPAddr  string
	GRPCAddr string
}

// RegisterRoutes wires application modules to protocol endpoints.
func RegisterRoutes(ctx context.Context, app *server.Server, database *gormstore.Database, config Config) error {
	if err := InitGormSchemas(ctx, database); err != nil {
		return err
	}

	httpEndpoint := app.HTTP(config.HTTPAddr)
	if err := RegisterHTTPModules(httpEndpoint, NewGORMStores(database)); err != nil {
		return err
	}
	MountOptionalProtocolModules(app, config)
	return nil
}
