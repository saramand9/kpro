// File: internal/modules/users/store/store.go
// Purpose: Adapts GORM to the users service persistence port.

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/saramand9/bignet/kpro/database/common/lifecycle"
	gormstore "github.com/saramand9/bignet/kpro/database/gorm"
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
	db, err := s.gormDB()
	if err != nil {
		return nil, err
	}
	var rows []userRow
	if err := db.WithContext(ctx).Order("id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return userRowsToDomain(rows), nil
}

// Find returns one user by id.
func (s *Store) Find(ctx context.Context, id int64) (users.User, error) {
	db, err := s.gormDB()
	if err != nil {
		return users.User{}, err
	}
	var row userRow
	if err := db.WithContext(ctx).First(&row, id).Error; err != nil {
		return users.User{}, normalizeFindError(err)
	}
	return userRowToDomain(row), nil
}

// Create inserts a user and returns the persisted row.
func (s *Store) Create(ctx context.Context, user users.User) (users.User, error) {
	db, err := s.gormDB()
	if err != nil {
		return users.User{}, err
	}
	row := userRowFromDomain(user)
	if err := db.WithContext(ctx).Create(&row).Error; err != nil {
		return users.User{}, err
	}
	return s.Find(ctx, row.ID)
}

// Update stores a user and returns the persisted row.
func (s *Store) Update(ctx context.Context, user users.User) (users.User, error) {
	db, err := s.gormDB()
	if err != nil {
		return users.User{}, err
	}
	row := userRowFromDomain(user)
	if err := db.WithContext(ctx).Save(&row).Error; err != nil {
		return users.User{}, err
	}
	return s.Find(ctx, user.ID)
}

// Delete removes one user by id.
func (s *Store) Delete(ctx context.Context, id int64) error {
	db, err := s.gormDB()
	if err != nil {
		return err
	}
	return db.WithContext(ctx).Delete(&userRow{}, id).Error
}

func (s *Store) gormDB() (*gorm.DB, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("%w: users gorm store is nil", lifecycle.ErrUnavailable)
	}
	return s.db, nil
}

func normalizeFindError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return sql.ErrNoRows
	}
	return err
}
