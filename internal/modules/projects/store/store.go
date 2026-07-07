// File: internal/modules/projects/store/store.go
// Purpose: Adapts database framework stores to the projects service persistence port.

package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/saramand9/bignet/kpro/database/dbx"
	gormstore "github.com/saramand9/bignet/kpro/database/gorm"
	"github.com/saramand9/bignet/kpro/internal/modules/projects"
	"gorm.io/gorm"
)

type frameworkStore interface {
	List(ctx context.Context) ([]projects.Project, error)
	Find(ctx context.Context, id any) (projects.Project, error)
	Create(ctx context.Context, value *projects.Project) (sql.Result, error)
	Update(ctx context.Context, value *projects.Project) (sql.Result, error)
	Delete(ctx context.Context, id any) (sql.Result, error)
}

// Store adapts a SQL-like framework store to project business semantics.
type Store struct {
	inner frameworkStore
}

// NewDBX creates a projects store backed by dbx.
func NewDBX(database *dbx.Database) *Store {
	return New(dbx.NewStore[projects.Project](database, projects.TableName))
}

// NewGORM creates a projects store backed by GORM.
func NewGORM(database *gormstore.Database) *Store {
	return New(gormstore.NewStore[projects.Project](database, projects.TableName))
}

// New adapts an existing framework store.
func New(inner frameworkStore) *Store {
	return &Store{inner: inner}
}

// List returns all projects.
func (s *Store) List(ctx context.Context) ([]projects.Project, error) {
	return s.inner.List(ctx)
}

// Find returns one project by id.
func (s *Store) Find(ctx context.Context, id int64) (projects.Project, error) {
	project, err := s.inner.Find(ctx, id)
	return project, normalizeFindError(err)
}

// Create inserts a project and returns the persisted row when the database reports an id.
func (s *Store) Create(ctx context.Context, project projects.Project) (projects.Project, error) {
	result, err := s.inner.Create(ctx, &project)
	if err != nil {
		return projects.Project{}, err
	}
	if id, err := result.LastInsertId(); err == nil && id > 0 {
		return s.Find(ctx, id)
	}
	return project, nil
}

// Update stores a project and returns the persisted row.
func (s *Store) Update(ctx context.Context, project projects.Project) (projects.Project, error) {
	if _, err := s.inner.Update(ctx, &project); err != nil {
		return projects.Project{}, err
	}
	return s.Find(ctx, project.ID)
}

// Delete removes one project by id.
func (s *Store) Delete(ctx context.Context, id int64) error {
	_, err := s.inner.Delete(ctx, id)
	return err
}

func normalizeFindError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return sql.ErrNoRows
	}
	return err
}
