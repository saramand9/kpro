package gormstore

import (
	"context"
	"database/sql"
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ConflictConfig configures upsert conflict handling.
type ConflictConfig struct {
	Columns       []string
	UpdateColumns []string
	DoNothing     bool
}

// Conflict creates an upsert conflict target.
func Conflict(columns ...string) ConflictConfig {
	return ConflictConfig{Columns: append([]string(nil), columns...)}
}

// Nothing creates an upsert conflict target that ignores conflicting rows.
func Nothing(columns ...string) ConflictConfig {
	return ConflictConfig{Columns: append([]string(nil), columns...), DoNothing: true}
}

// Update restricts upsert updates to the supplied columns.
func (c ConflictConfig) Update(columns ...string) ConflictConfig {
	c.UpdateColumns = append([]string(nil), columns...)
	return c
}

// Store provides common GORM-backed operations for one model/table.
type Store[T any] struct {
	db    *gorm.DB
	table string
}

// NewStore creates a GORM store from a Database resource.
func NewStore[T any](database *Database, table string) *Store[T] {
	if database == nil {
		return &Store[T]{table: table}
	}
	return NewStoreWithDB[T](database.GORM(), table)
}

// NewStoreFor creates a GORM store using T.TableName() when available, otherwise GORM naming.
func NewStoreFor[T any](database *Database) *Store[T] {
	return NewStore[T](database, modelTableName[T]())
}

// NewStoreWithDB creates a GORM store from a raw GORM handle.
func NewStoreWithDB[T any](db *gorm.DB, table string) *Store[T] {
	return &Store[T]{db: db, table: table}
}

// Table returns the configured table name.
func (s *Store[T]) Table() string {
	if s == nil {
		return ""
	}
	return s.table
}

// GORM returns the underlying GORM handle.
func (s *Store[T]) GORM() *gorm.DB {
	if s == nil {
		return nil
	}
	return s.db
}

// Create inserts value into the store table.
func (s *Store[T]) Create(ctx context.Context, value *T) (sql.Result, error) {
	db, err := s.tableDB(ctx)
	if err != nil {
		return nil, err
	}
	columns, err := insertColumns(value)
	if err != nil {
		return nil, err
	}
	tx := db.Select(columns).Create(value)
	return newResult(tx.RowsAffected, value), tx.Error
}

// Save creates value when it has no primary key, otherwise updates it.
func (s *Store[T]) Save(ctx context.Context, value *T) (sql.Result, error) {
	db, err := s.tableDB(ctx)
	if err != nil {
		return nil, err
	}
	tx := db.Save(value)
	return newResult(tx.RowsAffected, value), tx.Error
}

// Update updates value in the store table by primary key.
func (s *Store[T]) Update(ctx context.Context, value *T) (sql.Result, error) {
	db, err := s.tableDB(ctx)
	if err != nil {
		return nil, err
	}
	columns, err := mapRecord(value)
	if err != nil {
		return nil, err
	}
	setColumns, pkColumns := splitUpdateColumns(columns)
	if len(setColumns) == 0 {
		return nil, ErrNoColumns
	}
	if len(pkColumns) == 0 {
		return nil, ErrMissingPrimaryKey
	}
	tx := db.Where(columnMap(pkColumns)).Updates(columnMap(setColumns))
	return rowsResult(tx.RowsAffected), tx.Error
}

// Upsert inserts value or updates an existing row on conflict.
func (s *Store[T]) Upsert(ctx context.Context, value *T, conflict ConflictConfig) (sql.Result, error) {
	db, err := s.tableDB(ctx)
	if err != nil {
		return nil, err
	}
	columns, err := mapRecord(value)
	if err != nil {
		return nil, err
	}
	insertColumns := insertableColumns(columns)
	if len(insertColumns) == 0 {
		return nil, ErrNoColumns
	}
	if len(conflict.Columns) == 0 {
		for _, column := range columns {
			if column.primary {
				conflict.Columns = append(conflict.Columns, column.name)
			}
		}
	}
	if len(conflict.Columns) == 0 {
		return nil, ErrMissingPrimaryKey
	}
	updateColumns := conflict.UpdateColumns
	if len(updateColumns) == 0 && !conflict.DoNothing {
		for _, column := range insertColumns {
			if !containsName(conflict.Columns, column.name) {
				updateColumns = append(updateColumns, column.name)
			}
		}
	}
	insertNames := columnNames(insertColumns)
	if err := validateColumnNames(conflict.Columns...); err != nil {
		return nil, err
	}
	if err := validateColumnNames(updateColumns...); err != nil {
		return nil, err
	}
	if err := validateColumnNames(insertNames...); err != nil {
		return nil, err
	}
	onConflict := clause.OnConflict{Columns: clauseColumns(conflict.Columns)}
	if conflict.DoNothing || len(updateColumns) == 0 {
		onConflict.DoNothing = true
	} else {
		onConflict.DoUpdates = clause.AssignmentColumns(updateColumns)
	}
	tx := db.Clauses(onConflict).Select(insertNames).Create(value)
	return newResult(tx.RowsAffected, value), tx.Error
}

// BatchCreate inserts values into the store table.
func (s *Store[T]) BatchCreate(ctx context.Context, values []T) (sql.Result, error) {
	db, err := s.tableDB(ctx)
	if err != nil {
		return nil, err
	}
	if len(values) == 0 {
		return nil, ErrNoColumns
	}
	columns, err := insertColumns(values[0])
	if err != nil {
		return nil, err
	}
	tx := db.Select(columns).Create(&values)
	return rowsResult(tx.RowsAffected), tx.Error
}

// Delete removes one row by primary key id.
func (s *Store[T]) Delete(ctx context.Context, id any) (sql.Result, error) {
	db, err := s.tableDB(ctx)
	if err != nil {
		return nil, err
	}
	var model T
	tx := db.Delete(&model, id)
	return rowsResult(tx.RowsAffected), tx.Error
}

// Find returns one row by primary key id.
func (s *Store[T]) Find(ctx context.Context, id any) (T, error) {
	var value T
	db, err := s.tableDB(ctx)
	if err != nil {
		return value, err
	}
	tx := db.First(&value, id)
	return value, tx.Error
}

// GetByID returns one row by primary key id, or nil when it does not exist.
func (s *Store[T]) GetByID(ctx context.Context, id any) (*T, error) {
	value, err := s.Find(ctx, id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &value, nil
}

// List returns all rows for the store table.
func (s *Store[T]) List(ctx context.Context) ([]T, error) {
	db, err := s.tableDB(ctx)
	if err != nil {
		return nil, err
	}
	values := make([]T, 0)
	tx := db.Find(&values)
	return values, tx.Error
}

// FindOne returns one row matching filter, or nil when it does not exist.
func (s *Store[T]) FindOne(ctx context.Context, filter any) (*T, error) {
	db, err := s.tableDB(ctx)
	if err != nil {
		return nil, err
	}
	var value T
	tx := db
	if filter != nil {
		tx = tx.Where(filter)
	}
	err = tx.First(&value).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &value, nil
}

// FindAll returns all rows matching filter.
func (s *Store[T]) FindAll(ctx context.Context, filter any) ([]T, error) {
	db, err := s.tableDB(ctx)
	if err != nil {
		return nil, err
	}
	values := make([]T, 0)
	tx := db
	if filter != nil {
		tx = tx.Where(filter)
	}
	if err := tx.Find(&values).Error; err != nil {
		return nil, err
	}
	return values, nil
}

func (s *Store[T]) tableDB(ctx context.Context) (*gorm.DB, error) {
	if s == nil || s.db == nil {
		return nil, sql.ErrConnDone
	}
	if err := validateTableName(s.table); err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return s.db.WithContext(ctx).Table(s.table), nil
}

func insertColumns(record any) ([]string, error) {
	columns, err := mapRecord(record)
	if err != nil {
		return nil, err
	}
	insertColumns := insertableColumns(columns)
	if len(insertColumns) == 0 {
		return nil, ErrNoColumns
	}
	return columnNames(insertColumns), nil
}

func clauseColumns(names []string) []clause.Column {
	columns := make([]clause.Column, 0, len(names))
	for _, name := range names {
		columns = append(columns, clause.Column{Name: name})
	}
	return columns
}
