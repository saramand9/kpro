// File: dbx/store_gorm.go
// Purpose: Integrates generic Store CRUD operations with GORM when configured.

package dbx

import (
	"context"
	"database/sql"
	"errors"
	"reflect"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var errLastInsertIDUnavailable = errors.New("last insert id unavailable")

type gormResult struct {
	rowsAffected    int64
	lastInsertID    int64
	hasLastInsertID bool
}

func (r gormResult) LastInsertId() (int64, error) {
	if !r.hasLastInsertID {
		return 0, errLastInsertIDUnavailable
	}
	return r.lastInsertID, nil
}

func (r gormResult) RowsAffected() (int64, error) {
	return r.rowsAffected, nil
}

func (s *Store[T]) gormTable(ctx context.Context) (*gorm.DB, bool, error) {
	if s == nil || s.database == nil {
		return nil, true, sql.ErrConnDone
	}
	gormDB := s.database.GORM()
	if gormDB == nil {
		return nil, false, nil
	}
	if _, err := s.database.Dialect().quoteTable(s.table); err != nil {
		return nil, true, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return gormDB.WithContext(ctx).Table(s.table), true, nil
}

func (s *Store[T]) createWithGORM(ctx context.Context, value *T) (sql.Result, bool, error) {
	db, ok, err := s.gormTable(ctx)
	if !ok || err != nil {
		return nil, ok, err
	}
	columns, err := gormInsertColumns(value)
	if err != nil {
		return nil, true, err
	}
	if err := s.validateColumnNames(columns...); err != nil {
		return nil, true, err
	}
	tx := db.Select(columns).Create(value)
	return newGORMResult(tx.RowsAffected, value), true, tx.Error
}

func (s *Store[T]) updateWithGORM(ctx context.Context, value *T) (sql.Result, bool, error) {
	db, ok, err := s.gormTable(ctx)
	if !ok || err != nil {
		return nil, ok, err
	}
	columns, err := mapRecord(value)
	if err != nil {
		return nil, true, err
	}
	setColumns, pkColumns := splitUpdateColumns(columns)
	if len(setColumns) == 0 {
		return nil, true, ErrNoColumns
	}
	if len(pkColumns) == 0 {
		return nil, true, ErrMissingPrimaryKey
	}
	updates, err := s.columnMap(setColumns)
	if err != nil {
		return nil, true, err
	}
	where, err := s.columnMap(pkColumns)
	if err != nil {
		return nil, true, err
	}
	tx := db.Where(where).Updates(updates)
	return gormResult{rowsAffected: tx.RowsAffected}, true, tx.Error
}

func (s *Store[T]) upsertWithGORM(ctx context.Context, value *T, conflict ConflictConfig) (sql.Result, bool, error) {
	db, ok, err := s.gormTable(ctx)
	if !ok || err != nil {
		return nil, ok, err
	}
	columns, err := mapRecord(value)
	if err != nil {
		return nil, true, err
	}
	insertColumns := insertableColumns(columns)
	if len(insertColumns) == 0 {
		return nil, true, ErrNoColumns
	}
	if len(conflict.Columns) == 0 {
		for _, column := range columns {
			if column.primary {
				conflict.Columns = append(conflict.Columns, column.name)
			}
		}
	}
	if len(conflict.Columns) == 0 {
		return nil, true, ErrMissingPrimaryKey
	}
	updateColumns := conflict.UpdateColumns
	if len(updateColumns) == 0 && !conflict.DoNothing {
		for _, column := range insertColumns {
			if !containsName(conflict.Columns, column.name) {
				updateColumns = append(updateColumns, column.name)
			}
		}
	}
	if err := s.validateColumnNames(conflict.Columns...); err != nil {
		return nil, true, err
	}
	if err := s.validateColumnNames(updateColumns...); err != nil {
		return nil, true, err
	}
	insertNames := columnNames(insertColumns)
	if err := s.validateColumnNames(insertNames...); err != nil {
		return nil, true, err
	}
	onConflict := clause.OnConflict{Columns: gormClauseColumns(conflict.Columns)}
	if conflict.DoNothing || len(updateColumns) == 0 {
		onConflict.DoNothing = true
	} else {
		onConflict.DoUpdates = clause.AssignmentColumns(updateColumns)
	}
	tx := db.Clauses(onConflict).Select(insertNames).Create(value)
	return newGORMResult(tx.RowsAffected, value), true, tx.Error
}

func (s *Store[T]) batchCreateWithGORM(ctx context.Context, values []T) (sql.Result, bool, error) {
	db, ok, err := s.gormTable(ctx)
	if !ok || err != nil {
		return nil, ok, err
	}
	if len(values) == 0 {
		return nil, true, ErrNoColumns
	}
	columns, err := gormInsertColumns(values[0])
	if err != nil {
		return nil, true, err
	}
	if err := s.validateColumnNames(columns...); err != nil {
		return nil, true, err
	}
	tx := db.Select(columns).Create(&values)
	return gormResult{rowsAffected: tx.RowsAffected}, true, tx.Error
}

func (s *Store[T]) deleteWithGORM(ctx context.Context, id any) (sql.Result, bool, error) {
	db, ok, err := s.gormTable(ctx)
	if !ok || err != nil {
		return nil, ok, err
	}
	var model T
	tx := db.Delete(&model, id)
	return gormResult{rowsAffected: tx.RowsAffected}, true, tx.Error
}

func (s *Store[T]) columnMap(columns []columnValue) (map[string]any, error) {
	values := make(map[string]any, len(columns))
	for _, column := range columns {
		if err := s.validateColumnNames(column.name); err != nil {
			return nil, err
		}
		values[column.name] = column.value
	}
	return values, nil
}

func (s *Store[T]) validateColumnNames(names ...string) error {
	for _, name := range names {
		if _, err := s.database.Dialect().quoteColumn(name); err != nil {
			return err
		}
	}
	return nil
}

func gormInsertColumns(record any) ([]string, error) {
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

func columnNames(columns []columnValue) []string {
	names := make([]string, 0, len(columns))
	for _, column := range columns {
		names = append(names, column.name)
	}
	return names
}

func gormClauseColumns(names []string) []clause.Column {
	columns := make([]clause.Column, 0, len(names))
	for _, name := range names {
		columns = append(columns, clause.Column{Name: name})
	}
	return columns
}

func newGORMResult(rowsAffected int64, record any) sql.Result {
	lastInsertID, ok := primaryInt64(record)
	return gormResult{
		rowsAffected:    rowsAffected,
		lastInsertID:    lastInsertID,
		hasLastInsertID: ok,
	}
}

func primaryInt64(record any) (int64, bool) {
	columns, err := mapRecordSchema(record)
	if err != nil {
		return 0, false
	}
	column := primaryColumn(columns)
	if column.name == "" {
		return 0, false
	}
	return int64Value(column.value)
}

func int64Value(value any) (int64, bool) {
	if value == nil {
		return 0, false
	}
	reflected := reflect.ValueOf(value)
	for reflected.IsValid() && reflected.Kind() == reflect.Pointer {
		if reflected.IsNil() {
			return 0, false
		}
		reflected = reflected.Elem()
	}
	if !reflected.IsValid() {
		return 0, false
	}
	switch reflected.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return reflected.Int(), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		value := reflected.Uint()
		if value > uint64(^uint64(0)>>1) {
			return 0, false
		}
		return int64(value), true
	default:
		return 0, false
	}
}
