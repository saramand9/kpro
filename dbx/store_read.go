// File: dbx/store_read.go
// Purpose: Provides generic Store read and delete helpers.

package dbx

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"

	"gorm.io/gorm"
)

// Delete removes one row by primary key id.
func (s *Store[T]) Delete(ctx context.Context, id any) (sql.Result, error) {
	if s == nil || s.database == nil {
		return nil, sql.ErrConnDone
	}
	if gormDB := s.database.GORM(); gormDB != nil {
		var model T
		err := gormDB.WithContext(ctx).Table(s.table).Delete(&model, id).Error
		return nil, err
	}
	query, args, err := s.deleteSQL(id)
	if err != nil {
		return nil, err
	}
	return s.Writer().ExecContext(ctx, query, args...)
}

// Find returns one row by primary key id.
func (s *Store[T]) Find(ctx context.Context, id any) (T, error) {
	var value T
	if s == nil || s.database == nil {
		return value, sql.ErrConnDone
	}
	if gormDB := s.database.GORM(); gormDB != nil {
		err := gormDB.WithContext(ctx).Table(s.table).First(&value, id).Error
		return value, err
	}
	query, args, columns, err := s.selectSQL("WHERE " + s.primaryKeyWhere())
	if err != nil {
		return value, err
	}
	args = append(args, id)
	row := s.DB().QueryRowContext(ctx, query, args...)
	return value, scanRecord(row, &value, columns)
}

// List returns rows ordered by primary key when available.
func (s *Store[T]) List(ctx context.Context) ([]T, error) {
	if s == nil || s.database == nil {
		return nil, sql.ErrConnDone
	}
	if gormDB := s.database.GORM(); gormDB != nil {
		values, err := gorm.G[T](gormDB).Table(s.table).Find(ctx)
		return values, err
	}
	query, args, columns, err := s.selectSQL("")
	if err != nil {
		return nil, err
	}
	rows, err := s.DB().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	values := make([]T, 0)
	for rows.Next() {
		var value T
		if err := scanRecord(rows, &value, columns); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *Store[T]) deleteSQL(id any) (string, []any, error) {
	table, err := s.database.Dialect().quoteTable(s.table)
	if err != nil {
		return "", nil, err
	}
	where := s.primaryKeyWhere()
	return fmt.Sprintf("DELETE FROM %s WHERE %s", table, where), []any{id}, nil
}

func (s *Store[T]) selectSQL(where string) (string, []any, []columnValue, error) {
	var model T
	columns, err := mapRecord(model)
	if err != nil {
		return "", nil, nil, err
	}
	table, err := s.database.Dialect().quoteTable(s.table)
	if err != nil {
		return "", nil, nil, err
	}
	names, err := s.Writer().quotedColumnNames(columns)
	if err != nil {
		return "", nil, nil, err
	}
	query := fmt.Sprintf("SELECT %s FROM %s", strings.Join(names, ", "), table)
	if where != "" {
		query += " " + where
	}
	if pk := primaryColumn(columns); pk.name != "" {
		quoted, err := s.database.Dialect().quoteColumn(pk.name)
		if err != nil {
			return "", nil, nil, err
		}
		query += " ORDER BY " + quoted
	}
	return query, nil, columns, nil
}

func (s *Store[T]) primaryKeyWhere() string {
	var model T
	columns, err := mapRecord(model)
	if err == nil {
		if pk := primaryColumn(columns); pk.name != "" {
			quoted, quoteErr := s.database.Dialect().quoteColumn(pk.name)
			if quoteErr == nil {
				return quoted + " = " + s.database.Dialect().placeholder(1)
			}
		}
	}
	quoted, err := s.database.Dialect().quoteColumn("id")
	if err != nil {
		return "id = " + s.database.Dialect().placeholder(1)
	}
	return quoted + " = " + s.database.Dialect().placeholder(1)
}

func primaryColumn(columns []columnValue) columnValue {
	for _, column := range columns {
		if column.primary {
			return column
		}
	}
	return columnValue{}
}

type scanner interface {
	Scan(dest ...any) error
}

func scanRecord(row scanner, dest any, columns []columnValue) error {
	value := reflectValue(dest)
	if !value.IsValid() || value.Kind() != reflect.Struct {
		return fmt.Errorf("%w: scan destination must be struct pointer", ErrUnsupportedRead)
	}
	targets := make([]any, 0, len(columns))
	for _, column := range columns {
		field, ok := fieldByDBColumn(value, column.name)
		if !ok || !field.CanAddr() {
			return fmt.Errorf("%w: column %s has no scan target", ErrUnsupportedRead, column.name)
		}
		targets = append(targets, field.Addr().Interface())
	}
	return row.Scan(targets...)
}

func fieldByDBColumn(value reflect.Value, columnName string) (reflect.Value, bool) {
	valueType := value.Type()
	for i := 0; i < value.NumField(); i++ {
		field := valueType.Field(i)
		column, ok := parseDBTag(field)
		if !ok {
			continue
		}
		name := column.name
		if name == "" {
			name = toSnakeCase(field.Name)
		}
		if name == columnName {
			return value.Field(i), true
		}
	}
	return reflect.Value{}, false
}
