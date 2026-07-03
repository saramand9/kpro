// File: dbx/store.go
// Purpose: Provides generic Store helpers for application modules.

package dbx

import (
	"context"
	"database/sql"

	"gorm.io/gorm"
)

// StoreConfig controls a generic Store.
type StoreConfig struct {
	// Table is the database table name used by generated writes.
	Table string
}

// Store provides common write operations for one model/table.
type Store[T any] struct {
	database *Database
	table    string
}

// NewStore creates a generic Store for table.
func NewStore[T any](database *Database, table string) *Store[T] {
	return &Store[T]{
		database: database,
		table:    table,
	}
}

// Table returns the configured table name.
func (s *Store[T]) Table() string {
	if s == nil {
		return ""
	}
	return s.table
}

// DB returns the underlying database/sql pool.
func (s *Store[T]) DB() *sql.DB {
	if s == nil || s.database == nil {
		return nil
	}
	return s.database.DB()
}

// Writer returns the synchronous generated writer.
func (s *Store[T]) Writer() *Writer {
	if s == nil || s.database == nil {
		return nil
	}
	return s.database.Writer()
}

// Async returns the async store view.
func (s *Store[T]) Async() *AsyncStore[T] {
	return &AsyncStore[T]{store: s}
}

// GORM returns a typed GORM interface for this store's model.
func (s *Store[T]) GORM() gorm.Interface[T] {
	if s == nil || s.database == nil || s.database.GORM() == nil {
		return nil
	}
	return gorm.G[T](s.database.GORM())
}

// Create inserts value into the store table.
func (s *Store[T]) Create(ctx context.Context, value *T) (sql.Result, error) {
	if result, ok, err := s.createWithGORM(ctx, value); ok {
		return result, err
	}
	return s.Writer().Insert(ctx, s.table, value)
}

// Update updates value in the store table by primary key.
func (s *Store[T]) Update(ctx context.Context, value *T) (sql.Result, error) {
	if result, ok, err := s.updateWithGORM(ctx, value); ok {
		return result, err
	}
	return s.Writer().Update(ctx, s.table, value)
}

// Upsert inserts value or updates an existing row on conflict.
func (s *Store[T]) Upsert(ctx context.Context, value *T, conflict ConflictConfig) (sql.Result, error) {
	if result, ok, err := s.upsertWithGORM(ctx, value, conflict); ok {
		return result, err
	}
	return s.Writer().Upsert(ctx, s.table, value, conflict)
}

// BatchCreate inserts values into the store table.
func (s *Store[T]) BatchCreate(ctx context.Context, values []T) (sql.Result, error) {
	if result, ok, err := s.batchCreateWithGORM(ctx, values); ok {
		return result, err
	}
	return s.Writer().BatchInsert(ctx, s.table, values)
}

// AsyncStore provides async writes for Store.
type AsyncStore[T any] struct {
	store *Store[T]
}

// Create queues an insert into the store table.
func (s *AsyncStore[T]) Create(ctx context.Context, value *T) (*Future, error) {
	return s.store.database.Async().Submit(ctx, func(jobCtx context.Context) (sql.Result, error) {
		return s.store.Create(jobCtx, value)
	})
}

// Update queues an update into the store table.
func (s *AsyncStore[T]) Update(ctx context.Context, value *T) (*Future, error) {
	return s.store.database.Async().Submit(ctx, func(jobCtx context.Context) (sql.Result, error) {
		return s.store.Update(jobCtx, value)
	})
}

// Upsert queues an upsert into the store table.
func (s *AsyncStore[T]) Upsert(ctx context.Context, value *T, conflict ConflictConfig) (*Future, error) {
	return s.store.database.Async().Submit(ctx, func(jobCtx context.Context) (sql.Result, error) {
		return s.store.Upsert(jobCtx, value, conflict)
	})
}

// BatchCreate queues a batch insert into the store table.
func (s *AsyncStore[T]) BatchCreate(ctx context.Context, values []T) (*Future, error) {
	return s.store.database.Async().Submit(ctx, func(jobCtx context.Context) (sql.Result, error) {
		return s.store.BatchCreate(jobCtx, values)
	})
}
