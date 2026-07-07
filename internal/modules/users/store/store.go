// File: internal/modules/users/store/store.go
// Purpose: Adapts database framework stores to the users service persistence port.

package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/saramand9/bignet/kpro/database/dbx"
	gormstore "github.com/saramand9/bignet/kpro/database/gorm"
	"github.com/saramand9/bignet/kpro/internal/modules/users"
	"gorm.io/gorm"
)

type frameworkStore interface {
	List(ctx context.Context) ([]users.User, error)
	Find(ctx context.Context, id any) (users.User, error)
	Create(ctx context.Context, value *users.User) (sql.Result, error)
	Update(ctx context.Context, value *users.User) (sql.Result, error)
	Delete(ctx context.Context, id any) (sql.Result, error)
}

// Store adapts a SQL-like framework store to user business semantics.
type Store struct {
	inner frameworkStore
}

// NewDBX creates a users store backed by dbx.
func NewDBX(database *dbx.Database) *Store {
	return New(dbx.NewStore[users.User](database, users.TableName))
}

// NewGORM creates a users store backed by GORM.
func NewGORM(database *gormstore.Database) *Store {
	return New(gormstore.NewStore[users.User](database, users.TableName))
}

// New adapts an existing framework store.
func New(inner frameworkStore) *Store {
	return &Store{inner: inner}
}

// List returns all users.
func (s *Store) List(ctx context.Context) ([]users.User, error) {
	return s.inner.List(ctx)
}

// Find returns one user by id.
func (s *Store) Find(ctx context.Context, id int64) (users.User, error) {
	user, err := s.inner.Find(ctx, id)
	return user, normalizeFindError(err)
}

// Create inserts a user and returns the persisted row when the database reports an id.
func (s *Store) Create(ctx context.Context, user users.User) (users.User, error) {
	result, err := s.inner.Create(ctx, &user)
	if err != nil {
		return users.User{}, err
	}
	if id, err := result.LastInsertId(); err == nil && id > 0 {
		return s.Find(ctx, id)
	}
	return user, nil
}

// Update stores a user and returns the persisted row.
func (s *Store) Update(ctx context.Context, user users.User) (users.User, error) {
	if _, err := s.inner.Update(ctx, &user); err != nil {
		return users.User{}, err
	}
	return s.Find(ctx, user.ID)
}

// Delete removes one user by id.
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
