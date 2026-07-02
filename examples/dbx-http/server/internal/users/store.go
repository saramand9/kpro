// File: examples/dbx-http/server/internal/users/store.go
// Purpose: Provides runnable KPro database example code.

package users

import (
	"context"
	_ "embed"

	"github.com/saramand9/bignet/kpro/dbx"
)

//go:embed schema.sql
var schemaSQL string

// User is the HTTP response model and database row view.
type User struct {
	ID        int64  `json:"id" db:"id,pk,auto"`
	Name      string `json:"name" db:"name"`
	Age       int    `json:"age" db:"age"`
	CreatedAt string `json:"created_at" db:"created_at,omitempty"`
}

// Init creates example tables.
func Init(ctx context.Context, database *dbx.Database) error {
	_, err := database.Writer().ExecContext(ctx, schemaSQL)
	return err
}
