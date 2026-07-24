// File: internal/modules/projects/store/store.go
// Purpose: Adapts GORM to the projects service persistence port.

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/saramand9/kpro/database/common/lifecycle"
	gormstore "github.com/saramand9/kpro/database/gorm"
	"github.com/saramand9/kpro/internal/modules/projects"
	"gorm.io/gorm"
)

// Store adapts GORM to project business semantics.
type Store struct {
	db *gorm.DB
}

// NewGORM creates a projects store backed by GORM.
func NewGORM(database *gormstore.Database) *Store {
	if database == nil {
		return &Store{}
	}
	return &Store{db: database.GORM()}
}

// List returns all projects.
func (s *Store) List(ctx context.Context) ([]projects.Project, error) {
	db, err := s.gormDB()
	if err != nil {
		return nil, err
	}
	var rows []projectRow
	if err := db.WithContext(ctx).Order("id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return projectRowsToDomain(rows), nil
}

// Find returns one project by id.
func (s *Store) Find(ctx context.Context, id int64) (projects.Project, error) {
	db, err := s.gormDB()
	if err != nil {
		return projects.Project{}, err
	}
	var row projectRow
	if err := db.WithContext(ctx).First(&row, id).Error; err != nil {
		return projects.Project{}, normalizeFindError(err)
	}
	return projectRowToDomain(row), nil
}

// Create inserts a project and returns the persisted row.
func (s *Store) Create(ctx context.Context, project projects.Project) (projects.Project, error) {
	db, err := s.gormDB()
	if err != nil {
		return projects.Project{}, err
	}
	row := projectRowFromDomain(project)
	if err := db.WithContext(ctx).Create(&row).Error; err != nil {
		return projects.Project{}, err
	}
	return s.Find(ctx, row.ID)
}

// Update stores a project and returns the persisted row.
func (s *Store) Update(ctx context.Context, project projects.Project) (projects.Project, error) {
	db, err := s.gormDB()
	if err != nil {
		return projects.Project{}, err
	}
	row := projectRowFromDomain(project)
	if err := db.WithContext(ctx).Save(&row).Error; err != nil {
		return projects.Project{}, err
	}
	return s.Find(ctx, project.ID)
}

// Delete removes one project by id.
func (s *Store) Delete(ctx context.Context, id int64) error {
	db, err := s.gormDB()
	if err != nil {
		return err
	}
	return db.WithContext(ctx).Delete(&projectRow{}, id).Error
}

func (s *Store) gormDB() (*gorm.DB, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("%w: projects gorm store is nil", lifecycle.ErrUnavailable)
	}
	return s.db, nil
}

func normalizeFindError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return sql.ErrNoRows
	}
	return err
}
