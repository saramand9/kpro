// File: internal/modules/users/store/model.go
// Purpose: Defines GORM persistence rows for the users store.

package store

import (
	"context"
	"time"

	gormstore "github.com/saramand9/kpro/database/gorm"
	"github.com/saramand9/kpro/internal/modules/users"
)

type userRow struct {
	ID        int64     `gorm:"column:id;primaryKey;autoIncrement"`
	Name      string    `gorm:"column:name;not null"`
	Age       int       `gorm:"column:age;not null"`
	CreatedAt time.Time `gorm:"column:created_at;autoCreateTime"`
}

func (userRow) TableName() string {
	return users.TableName
}

// InitGORM applies users module schema migrations.
func InitGORM(ctx context.Context, database *gormstore.Database) error {
	return gormstore.ApplyMigrations(ctx, database, Migrations())
}

// Migrations returns versioned users schema migrations.
func Migrations() []gormstore.Migration {
	return []gormstore.Migration{
		{
			Version: 202607080001,
			Name:    "create_users",
			Statements: []gormstore.Statement{
				gormstore.DialectSQL(
					`CREATE TABLE IF NOT EXISTS users (
id BIGSERIAL PRIMARY KEY,
name TEXT NOT NULL,
age INTEGER NOT NULL,
created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
)`,
					map[string]string{
						"sqlite": `CREATE TABLE IF NOT EXISTS users (
id INTEGER PRIMARY KEY AUTOINCREMENT,
name TEXT NOT NULL,
age INTEGER NOT NULL,
created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
)`,
						"mysql": `CREATE TABLE IF NOT EXISTS users (
id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
name VARCHAR(255) NOT NULL,
age INT NOT NULL,
created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
)`,
					},
				),
			},
		},
	}
}

func userRowsToDomain(rows []userRow) []users.User {
	usersOut := make([]users.User, 0, len(rows))
	for _, row := range rows {
		usersOut = append(usersOut, userRowToDomain(row))
	}
	return usersOut
}

func userRowToDomain(row userRow) users.User {
	return users.User{
		ID:        row.ID,
		Name:      row.Name,
		Age:       row.Age,
		CreatedAt: row.CreatedAt,
	}
}

func userRowFromDomain(user users.User) userRow {
	return userRow{
		ID:        user.ID,
		Name:      user.Name,
		Age:       user.Age,
		CreatedAt: user.CreatedAt,
	}
}
