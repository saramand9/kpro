// File: internal/modules/projects/model.go
// Purpose: Defines the project entity model and schema initialization.

package projects

import (
	"context"
	"time"

	gormstore "github.com/saramand9/bignet/kpro/database/relational/gorm"
)

// TableName is the projects source-of-truth table.
const TableName = "projects"

// Project is the domain entity and GORM model for the projects table.
type Project struct {
	ID          int64     `json:"id" gorm:"column:id;primaryKey;autoIncrement"`
	Name        string    `json:"name" gorm:"column:name;not null"`
	Description string    `json:"description" gorm:"column:description;not null;default:''"`
	Status      string    `json:"status" gorm:"column:status;not null;default:'active'"`
	CreatedAt   time.Time `json:"created_at" gorm:"column:created_at;autoCreateTime"`
}

// TableName returns the GORM table name.
func (Project) TableName() string {
	return TableName
}

// Init creates or updates the projects module database schema with GORM.
func Init(ctx context.Context, database *gormstore.Database) error {
	return database.GORM().WithContext(ctx).AutoMigrate(&Project{})
}
