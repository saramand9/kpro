// File: internal/modules/projects/store/store.go
// Purpose: Adapts GORM to the projects service persistence port.

package store

import (
	"context"
	"database/sql"
	"errors"

	gormstore "github.com/saramand9/bignet/kpro/database/relational/gorm"
	"github.com/saramand9/bignet/kpro/internal/modules/projects"
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
	var rows []projects.Project
	if err := s.db.WithContext(ctx).Order("id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// Find returns one project by id.
func (s *Store) Find(ctx context.Context, id int64) (projects.Project, error) {
	var project projects.Project
	if err := s.db.WithContext(ctx).First(&project, id).Error; err != nil {
		return projects.Project{}, normalizeFindError(err)
	}
	return project, nil
}

// Create inserts a project and returns the persisted row.
func (s *Store) Create(ctx context.Context, project projects.Project) (projects.Project, error) {
	if err := s.db.WithContext(ctx).Create(&project).Error; err != nil {
		return projects.Project{}, err
	}
	return s.Find(ctx, project.ID)
}

// Update stores a project and returns the persisted row.
func (s *Store) Update(ctx context.Context, project projects.Project) (projects.Project, error) {
	if err := s.db.WithContext(ctx).Save(&project).Error; err != nil {
		return projects.Project{}, err
	}
	return s.Find(ctx, project.ID)
}

// Delete removes one project by id.
func (s *Store) Delete(ctx context.Context, id int64) error {
	return s.db.WithContext(ctx).Delete(&projects.Project{}, id).Error
}

func normalizeFindError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return sql.ErrNoRows
	}
	return err
}
