// File: examples/gorm-http/server/internal/app/schema.go
// Purpose: Centralizes application schema initialization for database examples.

package app

import (
	"context"

	gormstore "github.com/saramand9/bignet/kpro/database/relational/gorm"
	"github.com/saramand9/bignet/kpro/internal/modules/projects"
	"github.com/saramand9/bignet/kpro/internal/modules/users"
)

// InitGormSchemas initializes source-of-truth schemas for GORM-backed examples.
func InitGormSchemas(ctx context.Context, database *gormstore.Database) error {
	if err := users.Init(ctx, database); err != nil {
		return err
	}
	return projects.Init(ctx, database)
}
