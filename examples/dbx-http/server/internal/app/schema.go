// File: examples/dbx-http/server/internal/app/schema.go
// Purpose: Centralizes application schema initialization for database examples.

package app

import (
	"context"

	"github.com/saramand9/bignet/kpro/database/dbx"
	"github.com/saramand9/bignet/kpro/internal/modules/projects"
	"github.com/saramand9/bignet/kpro/internal/modules/users"
)

// InitDBXSchemas initializes source-of-truth schemas for dbx-backed examples.
func InitDBXSchemas(ctx context.Context, database *dbx.Database) error {
	if err := users.Init(ctx, database); err != nil {
		return err
	}
	return projects.Init(ctx, database)
}
