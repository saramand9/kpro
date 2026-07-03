// File: internal/modules/users/service/service.go
// Purpose: Provides user business operations over dbx stores.

package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/saramand9/bignet/kpro/dbx"
	"github.com/saramand9/bignet/kpro/internal/modules/users"
)

const tableName = "users"

// ErrInvalidInput reports invalid user service input.
var ErrInvalidInput = errors.New("invalid user input")

// Service owns user business operations and hides storage details from handlers.
type Service struct {
	store *dbx.Store[users.User]
}

// New creates a user service backed by database.
func New(database *dbx.Database) *Service {
	return NewWithStore(dbx.NewStore[users.User](database, tableName))
}

// NewWithStore creates a user service over an existing store.
func NewWithStore(store *dbx.Store[users.User]) *Service {
	return &Service{store: store}
}

// List returns all users.
func (s *Service) List(ctx context.Context) ([]users.User, error) {
	return s.store.List(ctx)
}

// Find returns one user by id.
func (s *Service) Find(ctx context.Context, id int64) (users.User, error) {
	return s.store.Find(ctx, id)
}

// Create validates and stores a new user, returning the persisted row.
func (s *Service) Create(ctx context.Context, input CreateInput) (users.User, error) {
	input.Name = strings.TrimSpace(input.Name)
	if err := input.validate(); err != nil {
		return users.User{}, err
	}
	user := users.User{
		Name: input.Name,
		Age:  input.Age,
	}
	result, err := s.store.Create(ctx, &user)
	if err != nil {
		return users.User{}, err
	}
	if id, err := result.LastInsertId(); err == nil && id > 0 {
		return s.Find(ctx, id)
	}
	return user, nil
}

// Update applies a partial update and returns the persisted row.
func (s *Service) Update(ctx context.Context, id int64, input UpdateInput) (users.User, error) {
	current, err := s.Find(ctx, id)
	if err != nil {
		return users.User{}, err
	}
	updated, err := input.apply(current)
	if err != nil {
		return users.User{}, err
	}
	if _, err := s.store.Update(ctx, &updated); err != nil {
		return users.User{}, err
	}
	return s.Find(ctx, id)
}

// Delete removes a user by id.
func (s *Service) Delete(ctx context.Context, id int64) error {
	_, err := s.store.Delete(ctx, id)
	return err
}

func (i CreateInput) validate() error {
	if strings.TrimSpace(i.Name) == "" {
		return fmt.Errorf("%w: name is required", ErrInvalidInput)
	}
	if i.Age < 0 {
		return fmt.Errorf("%w: age must be non-negative", ErrInvalidInput)
	}
	return nil
}

func (i UpdateInput) apply(user users.User) (users.User, error) {
	if i.Name == nil && i.Age == nil {
		return user, fmt.Errorf("%w: name or age is required", ErrInvalidInput)
	}
	if i.Name != nil {
		name := strings.TrimSpace(*i.Name)
		if name == "" {
			return user, fmt.Errorf("%w: name is required", ErrInvalidInput)
		}
		user.Name = name
	}
	if i.Age != nil {
		if *i.Age < 0 {
			return user, fmt.Errorf("%w: age must be non-negative", ErrInvalidInput)
		}
		user.Age = *i.Age
	}
	return user, nil
}
