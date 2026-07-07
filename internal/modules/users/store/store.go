// File: internal/modules/users/store/store.go
// Purpose: Adapts GORM to the users service persistence port.

package store

import (
	"context"
	"database/sql"
	"errors"

	gormstore "github.com/saramand9/bignet/kpro/database/relational/gorm"
	"github.com/saramand9/bignet/kpro/internal/modules/users"
	"gorm.io/gorm"
)

// Store adapts GORM to user business semantics.
type Store struct {
	db *gorm.DB
}

// NewGORM creates a users store backed by GORM.
func NewGORM(database *gormstore.Database) *Store {
	if database == nil {
		return &Store{}
	}
	return &Store{db: database.GORM()}
}

// List returns all users.
func (s *Store) List(ctx context.Context) ([]users.User, error) {
	var rows []users.User
	if err := s.db.WithContext(ctx).Order("id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// Find returns one user by id.
func (s *Store) Find(ctx context.Context, id int64) (users.User, error) {
	var user users.User
	if err := s.db.WithContext(ctx).First(&user, id).Error; err != nil {
		return users.User{}, normalizeFindError(err)
	}
	return user, nil
}

// Create inserts a user and returns the persisted row.
func (s *Store) Create(ctx context.Context, user users.User) (users.User, error) {
	if err := s.db.WithContext(ctx).Create(&user).Error; err != nil {
		return users.User{}, err
	}
	return s.Find(ctx, user.ID)
}

// Update stores a user and returns the persisted row.
func (s *Store) Update(ctx context.Context, user users.User) (users.User, error) {
	if err := s.db.WithContext(ctx).Save(&user).Error; err != nil {
		return users.User{}, err
	}
	return s.Find(ctx, user.ID)
}

// Delete removes one user by id.
func (s *Store) Delete(ctx context.Context, id int64) error {
	return s.db.WithContext(ctx).Delete(&users.User{}, id).Error
}

func normalizeFindError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return sql.ErrNoRows
	}
	return err
}
