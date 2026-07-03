// File: examples/dbx-http/server/internal/users/service.go
// Purpose: Provides user business operations over dbx entities.

package users

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/saramand9/bignet/kpro/appx"
	"github.com/saramand9/bignet/kpro/dbx"
)

const tableName = "users"

// ErrInvalidInput reports invalid user service input.
var ErrInvalidInput = errors.New("invalid user input")

// CreateInput is the service input for creating a user.
type CreateInput struct {
	Name string
	Age  int
}

// UpdateInput is the service input for updating a user.
type UpdateInput struct {
	Name *string
	Age  *int
}

// Service owns user business operations and hides storage details from HTTP.
type Service struct {
	entity *appx.Entity[User]
}

// NewService creates a user service backed by database.
func NewService(database *dbx.Database) *Service {
	entity := appx.NewEntity[User](database, appx.EntityConfig{Table: tableName})
	return NewServiceWithEntity(entity)
}

// NewServiceWithEntity creates a user service over an existing entity.
func NewServiceWithEntity(entity *appx.Entity[User]) *Service {
	return &Service{entity: entity}
}

// List returns all users.
func (s *Service) List(ctx context.Context) ([]User, error) {
	return s.entity.List(ctx)
}

// Find returns one user by id.
func (s *Service) Find(ctx context.Context, id int64) (User, error) {
	return s.entity.Find(ctx, id)
}

// Create validates and stores a new user, returning the persisted row.
func (s *Service) Create(ctx context.Context, input CreateInput) (User, error) {
	input.Name = strings.TrimSpace(input.Name)
	if err := input.validate(); err != nil {
		return User{}, err
	}
	user := User{
		Name: input.Name,
		Age:  input.Age,
	}
	result, err := s.entity.Store().Create(ctx, &user)
	if err != nil {
		return User{}, err
	}
	if id, err := result.LastInsertId(); err == nil && id > 0 {
		return s.Find(ctx, id)
	}
	return user, nil
}

// Update applies a partial update and returns the persisted row.
func (s *Service) Update(ctx context.Context, id int64, input UpdateInput) (User, error) {
	current, err := s.Find(ctx, id)
	if err != nil {
		return User{}, err
	}
	updated, err := input.apply(current)
	if err != nil {
		return User{}, err
	}
	if _, err := s.entity.Store().Update(ctx, &updated); err != nil {
		return User{}, err
	}
	return s.Find(ctx, id)
}

// Delete removes a user by id.
func (s *Service) Delete(ctx context.Context, id int64) error {
	_, err := s.entity.Store().Delete(ctx, id)
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

func (i UpdateInput) apply(user User) (User, error) {
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
