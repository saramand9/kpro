// File: internal/modules/projects/model.go
// Purpose: Defines the project entity model and schema initialization.

package projects

import (
	"context"
	_ "embed"

	"github.com/saramand9/bignet/kpro/dbx"
)

//go:embed schema.sql
var schemaSQL string

// Project is the domain entity and database row view for the projects module.
type Project struct {
	ID          int64  `json:"id" db:"id,pk,auto" gorm:"column:id;primaryKey;autoIncrement"`
	Name        string `json:"name" db:"name" gorm:"column:name"`
	Description string `json:"description" db:"description" gorm:"column:description"`
	Status      string `json:"status" db:"status" gorm:"column:status"`
	CreatedAt   string `json:"created_at" db:"created_at,omitempty" gorm:"column:created_at;->"`
}

// Init creates the projects module database schema.
func Init(ctx context.Context, database *dbx.Database) error {
	_, err := database.Writer().ExecContext(ctx, schemaSQL)
	return err
}
