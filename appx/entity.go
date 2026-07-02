// File: appx/entity.go
// Purpose: Provides model-centric Entity helpers for common resources.

package appx

import (
	"context"
	"database/sql"

	"github.com/saramand9/bignet/kpro/dbx"
)

// EntityConfig controls an Entity.
type EntityConfig struct {
	// Table is the database table name used by the entity store.
	Table string
}

// Entity is the model-centric entry point for common CRUD resources.
type Entity[T any] struct {
	store   *dbx.Store[T]
	service *CRUDService[T]
}

// NewEntity creates an Entity backed by database and config.Table.
func NewEntity[T any](database *dbx.Database, config EntityConfig) *Entity[T] {
	store := dbx.NewStore[T](database, config.Table)
	return NewEntityWithStore(store)
}

// NewEntityWithStore creates an Entity over an existing store.
func NewEntityWithStore[T any](store *dbx.Store[T]) *Entity[T] {
	return &Entity[T]{
		store:   store,
		service: NewCRUDService[T](store),
	}
}

// Store returns the entity's data store.
func (e *Entity[T]) Store() *dbx.Store[T] {
	if e == nil {
		return nil
	}
	return e.store
}

// Service returns the entity's generic CRUD service.
func (e *Entity[T]) Service() *CRUDService[T] {
	if e == nil {
		return nil
	}
	return e.service
}

// Create inserts value.
func (e *Entity[T]) Create(ctx context.Context, value *T) (T, error) {
	return e.service.Create(ctx, value)
}

// Update updates value.
func (e *Entity[T]) Update(ctx context.Context, value *T) (T, error) {
	return e.service.Update(ctx, value)
}

// Upsert inserts or updates value.
func (e *Entity[T]) Upsert(ctx context.Context, value *T, conflict dbx.ConflictConfig) (T, error) {
	return e.service.Upsert(ctx, value, conflict)
}

// Delete removes a value by id.
func (e *Entity[T]) Delete(ctx context.Context, id any) error {
	return e.service.Delete(ctx, id)
}

// Find returns a value by id.
func (e *Entity[T]) Find(ctx context.Context, id any) (T, error) {
	return e.service.Find(ctx, id)
}

// List returns all values.
func (e *Entity[T]) List(ctx context.Context) ([]T, error) {
	return e.service.List(ctx)
}

// BatchCreate inserts values through the underlying store.
func (e *Entity[T]) BatchCreate(ctx context.Context, values []T) (sql.Result, error) {
	return e.store.BatchCreate(ctx, values)
}
