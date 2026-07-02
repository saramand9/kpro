// File: examples/dbx-http/server/internal/app/routes.go
// Purpose: Provides runnable KPro database example code.

package app

import (
	"context"

	"github.com/saramand9/bignet/kpro/appx"
	"github.com/saramand9/bignet/kpro/dbx"
	"github.com/saramand9/bignet/kpro/examples/dbx-http/server/internal/users"
	"github.com/saramand9/bignet/kpro/server"
)

// Config controls mounted routes for the dbx HTTP example.
type Config struct {
	HTTPAddr string
}

// RegisterRoutes wires a generic CRUD handler to a dbx Store.
func RegisterRoutes(ctx context.Context, app *server.Server, database *dbx.Database, config Config) error {
	if err := users.Init(ctx, database); err != nil {
		return err
	}
	entity := appx.NewEntity[users.User](database, appx.EntityConfig{Table: "users"})
	appx.BindHTTPEntity(app.HTTP(config.HTTPAddr), entity, appx.HTTPEntityConfig{Path: "/users"})
	return nil
}
