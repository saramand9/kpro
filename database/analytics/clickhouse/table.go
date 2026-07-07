package clickhousestore

import (
	"context"
	"database/sql"

	"github.com/saramand9/bignet/kpro/database/contracts/readmodels"
)

// WriterFunc writes one analytical event.
type WriterFunc[T any] func(ctx context.Context, db *sql.DB, value T) error

// BatchWriterFunc writes analytical events in a caller-defined batch strategy.
type BatchWriterFunc[T any] func(ctx context.Context, db *sql.DB, values []T) error

// AnalyticsTable adapts caller-owned ClickHouse write SQL to readmodels.AnalyticsTable.
type AnalyticsTable[T any] struct {
	database   *Database
	name       string
	writeOne   WriterFunc[T]
	writeBatch BatchWriterFunc[T]
}

var _ readmodels.AnalyticsTable[any] = (*AnalyticsTable[any])(nil)

// NewAnalyticsTable creates a typed analytical table view.
func NewAnalyticsTable[T any](database *Database, name string, writeOne WriterFunc[T], writeBatch BatchWriterFunc[T]) *AnalyticsTable[T] {
	return &AnalyticsTable[T]{
		database:   database,
		name:       name,
		writeOne:   writeOne,
		writeBatch: writeBatch,
	}
}

// Name returns the logical table name.
func (t *AnalyticsTable[T]) Name() string {
	if t == nil {
		return ""
	}
	return t.name
}

// Write implements readmodels.AnalyticsTable.
func (t *AnalyticsTable[T]) Write(ctx context.Context, value T) error {
	if t == nil || t.database == nil || t.database.db == nil {
		return sql.ErrConnDone
	}
	if t.writeOne == nil {
		return ErrInvalidWriter
	}
	return t.writeOne(contextOrBackground(ctx), t.database.db, value)
}

// WriteBatch implements readmodels.AnalyticsTable.
func (t *AnalyticsTable[T]) WriteBatch(ctx context.Context, values []T) error {
	if len(values) == 0 {
		return nil
	}
	if t == nil || t.database == nil || t.database.db == nil {
		return sql.ErrConnDone
	}
	if t.writeBatch != nil {
		return t.writeBatch(contextOrBackground(ctx), t.database.db, values)
	}
	if t.writeOne == nil {
		return ErrInvalidWriter
	}
	for _, value := range values {
		if err := t.writeOne(contextOrBackground(ctx), t.database.db, value); err != nil {
			return err
		}
	}
	return nil
}
