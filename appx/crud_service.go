// File: appx/crud_service.go
// Purpose: Provides generic CRUD services for common resources.

package appx

import (
	"context"
	"database/sql"

	"github.com/saramand9/bignet/kpro/dbx"
)

// CRUDStore is the storage contract used by CRUDService.
type CRUDStore[T any] interface {
	Create(context.Context, *T) (sql.Result, error)
	Update(context.Context, *T) (sql.Result, error)
	Upsert(context.Context, *T, dbx.ConflictConfig) (sql.Result, error)
	Delete(context.Context, any) (sql.Result, error)
	Find(context.Context, any) (T, error)
	List(context.Context) ([]T, error)
}

// CRUDService provides standard create, read, update, delete operations.
type CRUDService[T any] struct {
	store CRUDStore[T]
}

// NewCRUDService creates a CRUD service over store.
func NewCRUDService[T any](store CRUDStore[T]) *CRUDService[T] {
	return &CRUDService[T]{store: store}
}

// Store returns the wrapped store.
func (s *CRUDService[T]) Store() CRUDStore[T] {
	if s == nil {
		return nil
	}
	return s.store
}

// Create inserts value.
func (s *CRUDService[T]) Create(ctx context.Context, value *T) (T, error) {
	if _, err := s.store.Create(ctx, value); err != nil {
		var zero T
		return zero, err
	}
	return *value, nil
}

// Update updates value.
func (s *CRUDService[T]) Update(ctx context.Context, value *T) (T, error) {
	if _, err := s.store.Update(ctx, value); err != nil {
		var zero T
		return zero, err
	}
	return *value, nil
}

// Upsert inserts or updates value.
func (s *CRUDService[T]) Upsert(ctx context.Context, value *T, conflict dbx.ConflictConfig) (T, error) {
	if _, err := s.store.Upsert(ctx, value, conflict); err != nil {
		var zero T
		return zero, err
	}
	return *value, nil
}

// Delete removes a value by id.
func (s *CRUDService[T]) Delete(ctx context.Context, id any) error {
	_, err := s.store.Delete(ctx, id)
	return err
}

// Find returns a value by id.
func (s *CRUDService[T]) Find(ctx context.Context, id any) (T, error) {
	return s.store.Find(ctx, id)
}

// List returns all values.
func (s *CRUDService[T]) List(ctx context.Context) ([]T, error) {
	return s.store.List(ctx)
}
