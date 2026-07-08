// File: examples/gorm-http/server/internal/app/schema.go
// Purpose: Centralizes application schema initialization for database examples.

package app

import (
	"context"

	gormstore "github.com/saramand9/bignet/kpro/database/gorm"
	projectstore "github.com/saramand9/bignet/kpro/internal/modules/projects/store"
	userstore "github.com/saramand9/bignet/kpro/internal/modules/users/store"
)

// InitGormSchemas initializes source-of-truth schemas for GORM-backed examples.
func InitGormSchemas(ctx context.Context, database *gormstore.Database) error {
	if err := userstore.InitGORM(ctx, database); err != nil {
		return err
	}
	return projectstore.InitGORM(ctx, database)
}
