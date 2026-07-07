// File: internal/modules/projects/model.go
// Purpose: Defines the project entity model and schema initialization.

package projects

import (
	"context"
	_ "embed"

	"github.com/saramand9/bignet/kpro/database/dbx"
	sqlstore "github.com/saramand9/bignet/kpro/database/sql"
)

//go:embed schema.sql
var schemaSQL string

// TableName is the projects source-of-truth table.
const TableName = "projects"

// Project is the domain entity and database row view for the projects module.
type Project struct {
	ID          int64  `json:"id" db:"id,pk,auto"`
	Name        string `json:"name" db:"name"`
	Description string `json:"description" db:"description"`
	Status      string `json:"status" db:"status"`
	CreatedAt   string `json:"created_at" db:"created_at,omitempty"`
}

// Init creates the projects module database schema.
func Init(ctx context.Context, database *dbx.Database) error {
	return sqlstore.NewMigrator(database, "").Apply(ctx, Migration())
}

// Migration returns the projects module schema migration.
func Migration() sqlstore.Migration {
	return sqlstore.Migration{
		ID:         "projects_001_create",
		Statements: []string{schemaSQL},
	}
}
