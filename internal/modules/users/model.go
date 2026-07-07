// File: internal/modules/users/model.go
// Purpose: Defines the user entity model and schema initialization.

package users

import (
	"context"
	"time"

	gormstore "github.com/saramand9/bignet/kpro/database/relational/gorm"
)

// TableName is the users source-of-truth table.
const TableName = "users"

// User is the domain entity and GORM model for the users table.
type User struct {
	ID        int64     `json:"id" gorm:"column:id;primaryKey;autoIncrement"`
	Name      string    `json:"name" gorm:"column:name;not null"`
	Age       int       `json:"age" gorm:"column:age;not null"`
	CreatedAt time.Time `json:"created_at" gorm:"column:created_at;autoCreateTime"`
}

// TableName returns the GORM table name.
func (User) TableName() string {
	return TableName
}

// Init creates or updates the users module database schema with GORM.
func Init(ctx context.Context, database *gormstore.Database) error {
	return database.GORM().WithContext(ctx).AutoMigrate(&User{})
}
