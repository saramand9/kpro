// File: database/dbx/store.go
// Purpose: Provides generic Store helpers for application modules.

package dbx

import (
	"context"
	"database/sql"
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

// Create inserts value into the store table.
func (s *Store[T]) Create(ctx context.Context, value *T) (sql.Result, error) {
	writer, err := s.writerForContext(ctx)
	if err != nil {
		return nil, err
	}
	return writer.Insert(ctx, s.table, value)
}

// Update updates value in the store table by primary key.
func (s *Store[T]) Update(ctx context.Context, value *T) (sql.Result, error) {
	writer, err := s.writerForContext(ctx)
	if err != nil {
		return nil, err
	}
	return writer.Update(ctx, s.table, value)
}

// Upsert inserts value or updates an existing row on conflict.
func (s *Store[T]) Upsert(ctx context.Context, value *T, conflict ConflictConfig) (sql.Result, error) {
	writer, err := s.writerForContext(ctx)
	if err != nil {
		return nil, err
	}
	return writer.Upsert(ctx, s.table, value, conflict)
}

// BatchCreate inserts values into the store table.
func (s *Store[T]) BatchCreate(ctx context.Context, values []T) (sql.Result, error) {
	writer, err := s.writerForContext(ctx)
	if err != nil {
		return nil, err
	}
	return writer.BatchInsert(ctx, s.table, values)
}

func (s *Store[T]) writerForContext(ctx context.Context) (*Writer, error) {
	if s == nil || s.database == nil {
		return nil, sql.ErrConnDone
	}
	if tx, ok := TxFromContext(ctx); ok && tx.Writer() != nil {
		return tx.Writer(), nil
	}
	writer := s.database.Writer()
	if writer == nil {
		return nil, sql.ErrConnDone
	}
	return writer, nil
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
