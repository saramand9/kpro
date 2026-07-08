// File: internal/modules/projects/store/model.go
// Purpose: Defines GORM persistence rows for the projects store.

package store

import (
	"context"
	"time"

	gormstore "github.com/saramand9/bignet/kpro/database/gorm"
	"github.com/saramand9/bignet/kpro/internal/modules/projects"
)

type projectRow struct {
	ID          int64     `gorm:"column:id;primaryKey;autoIncrement"`
	Name        string    `gorm:"column:name;not null"`
	Description string    `gorm:"column:description;not null;default:''"`
	Status      string    `gorm:"column:status;not null;default:'active'"`
	CreatedAt   time.Time `gorm:"column:created_at;autoCreateTime"`
}

func (projectRow) TableName() string {
	return projects.TableName
}

// InitGORM applies projects module schema migrations.
func InitGORM(ctx context.Context, database *gormstore.Database) error {
	return gormstore.ApplyMigrations(ctx, database, Migrations())
}

// Migrations returns versioned projects schema migrations.
func Migrations() []gormstore.Migration {
	return []gormstore.Migration{
		{
			Version: 202607080101,
			Name:    "create_projects",
			Statements: []gormstore.Statement{
				gormstore.DialectSQL(
					`CREATE TABLE IF NOT EXISTS projects (
id BIGSERIAL PRIMARY KEY,
name TEXT NOT NULL,
description TEXT NOT NULL DEFAULT '',
status VARCHAR(32) NOT NULL DEFAULT 'active',
created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
)`,
					map[string]string{
						"sqlite": `CREATE TABLE IF NOT EXISTS projects (
id INTEGER PRIMARY KEY AUTOINCREMENT,
name TEXT NOT NULL,
description TEXT NOT NULL DEFAULT '',
status TEXT NOT NULL DEFAULT 'active',
created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
)`,
						"mysql": `CREATE TABLE IF NOT EXISTS projects (
id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
name VARCHAR(255) NOT NULL,
description TEXT NOT NULL,
status VARCHAR(32) NOT NULL DEFAULT 'active',
created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
)`,
					},
				),
			},
		},
	}
}

func projectRowsToDomain(rows []projectRow) []projects.Project {
	projectsOut := make([]projects.Project, 0, len(rows))
	for _, row := range rows {
		projectsOut = append(projectsOut, projectRowToDomain(row))
	}
	return projectsOut
}

func projectRowToDomain(row projectRow) projects.Project {
	return projects.Project{
		ID:          row.ID,
		Name:        row.Name,
		Description: row.Description,
		Status:      row.Status,
		CreatedAt:   row.CreatedAt,
	}
}

func projectRowFromDomain(project projects.Project) projectRow {
	return projectRow{
		ID:          project.ID,
		Name:        project.Name,
		Description: project.Description,
		Status:      project.Status,
		CreatedAt:   project.CreatedAt,
	}
}
