// File: examples/gorm-http/server/internal/app/routes.go
// Purpose: Provides runnable KPro database example code.

package app

import (
	"context"

	gormstore "github.com/saramand9/kpro/database/gorm"
	"github.com/saramand9/kpro/server"
	"github.com/saramand9/kpro/transport/httpx"
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

	httpEndpoint := httpx.New(httpx.Config{Addr: config.HTTPAddr})
	app.MountRunner(httpEndpoint)
	stores := NewGORMStores(database)
	if err := RegisterHTTPModules(httpEndpoint, stores); err != nil {
		return err
	}
	MountOptionalProtocolModules(app, config, stores)
	return nil
}
