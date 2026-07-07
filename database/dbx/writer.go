// File: database/dbx/writer.go
// Purpose: Provides synchronous generated database writes.

package dbx

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"time"
)

type sqlExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// WriterConfig controls Writer behavior.
type WriterConfig struct {
	// WriteTimeout bounds each generated write. Values <= 0 leave the caller context unchanged.
	WriteTimeout time.Duration
}

// Writer executes generated write statements through database/sql.
type Writer struct {
	exec         sqlExecer
	dialect      Dialect
	writeTimeout time.Duration
}

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

// NewWriter creates a Writer over any ExecContext-compatible database handle.
func NewWriter(exec sqlExecer, dialect Dialect, config WriterConfig) *Writer {
	return &Writer{
		exec:         exec,
		dialect:      dialect,
		writeTimeout: config.WriteTimeout,
	}
}

// ExecContext exposes raw database/sql execution for operations outside generated writes.
func (w *Writer) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if w == nil || w.exec == nil {
		return nil, sql.ErrConnDone
	}
	ctx, cancel := w.writeContext(ctx)
	defer cancel()
	return w.exec.ExecContext(ctx, query, args...)
}

// Insert writes one record into table.
func (w *Writer) Insert(ctx context.Context, table string, record any) (sql.Result, error) {
	query, args, err := w.insertSQL(table, record)
	if err != nil {
		return nil, err
	}
	return w.ExecContext(ctx, query, args...)
}

// Update updates one record by its primary key columns.
func (w *Writer) Update(ctx context.Context, table string, record any) (sql.Result, error) {
	query, args, err := w.updateSQL(table, record)
	if err != nil {
		return nil, err
	}
	return w.ExecContext(ctx, query, args...)
}

// Upsert inserts one record or updates an existing row on conflict.
func (w *Writer) Upsert(ctx context.Context, table string, record any, conflict ConflictConfig) (sql.Result, error) {
	query, args, err := w.upsertSQL(table, record, conflict)
	if err != nil {
		return nil, err
	}
	return w.ExecContext(ctx, query, args...)
}

// BatchInsert inserts multiple records in one statement.
func (w *Writer) BatchInsert(ctx context.Context, table string, records any) (sql.Result, error) {
	query, args, err := w.batchInsertSQL(table, records)
	if err != nil {
		return nil, err
	}
	return w.ExecContext(ctx, query, args...)
}

func (w *Writer) insertSQL(table string, record any) (string, []any, error) {
	columns, err := mapRecord(record)
	if err != nil {
		return "", nil, err
	}
	columns = insertableColumns(columns)
	return w.buildInsertSQL(table, columns)
}

func (w *Writer) updateSQL(table string, record any) (string, []any, error) {
	columns, err := mapRecord(record)
	if err != nil {
		return "", nil, err
	}
	return w.buildUpdateSQL(table, columns)
}

func (w *Writer) upsertSQL(table string, record any, conflict ConflictConfig) (string, []any, error) {
	columns, err := mapRecord(record)
	if err != nil {
		return "", nil, err
	}
	insertColumns := insertableColumns(columns)
	if len(conflict.Columns) == 0 {
		for _, column := range columns {
			if column.primary {
				conflict.Columns = append(conflict.Columns, column.name)
			}
		}
	}
	if len(conflict.Columns) == 0 {
		return "", nil, ErrMissingPrimaryKey
	}
	updateColumns := conflict.UpdateColumns
	if len(updateColumns) == 0 && !conflict.DoNothing {
		for _, column := range insertColumns {
			if !containsName(conflict.Columns, column.name) {
				updateColumns = append(updateColumns, column.name)
			}
		}
	}
	return w.buildUpsertSQL(table, insertColumns, conflict.Columns, updateColumns, conflict.DoNothing)
}

func (w *Writer) batchInsertSQL(table string, records any) (string, []any, error) {
	value := reflectValue(records)
	if !value.IsValid() || (value.Kind() != reflect.Slice && value.Kind() != reflect.Array) {
		return "", nil, fmt.Errorf("%w: batch insert expects slice or array", ErrNoColumns)
	}
	if value.Len() == 0 {
		return "", nil, ErrNoColumns
	}
	firstColumns, err := mapRecord(value.Index(0).Interface())
	if err != nil {
		return "", nil, err
	}
	firstColumns = insertableColumns(firstColumns)
	if len(firstColumns) == 0 {
		return "", nil, ErrNoColumns
	}
	rows := make([][]columnValue, 0, value.Len())
	rows = append(rows, firstColumns)
	for i := 1; i < value.Len(); i++ {
		columns, err := mapRecord(value.Index(i).Interface())
		if err != nil {
			return "", nil, err
		}
		rows = append(rows, alignColumns(firstColumns, insertableColumns(columns)))
	}
	return w.buildBatchInsertSQL(table, firstColumns, rows)
}

func (w *Writer) buildInsertSQL(table string, columns []columnValue) (string, []any, error) {
	if len(columns) == 0 {
		return "", nil, ErrNoColumns
	}
	tableName, err := w.dialect.quoteTable(table)
	if err != nil {
		return "", nil, err
	}
	columnNames, err := w.quotedColumnNames(columns)
	if err != nil {
		return "", nil, err
	}
	placeholders := make([]string, 0, len(columns))
	args := make([]any, 0, len(columns))
	for i, column := range columns {
		placeholders = append(placeholders, w.dialect.placeholder(i+1))
		args = append(args, column.value)
	}
	query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		tableName,
		strings.Join(columnNames, ", "),
		strings.Join(placeholders, ", "),
	)
	return query, args, nil
}

func (w *Writer) buildUpdateSQL(table string, columns []columnValue) (string, []any, error) {
	tableName, err := w.dialect.quoteTable(table)
	if err != nil {
		return "", nil, err
	}
	setColumns, pkColumns := splitUpdateColumns(columns)
	if len(setColumns) == 0 {
		return "", nil, ErrNoColumns
	}
	if len(pkColumns) == 0 {
		return "", nil, ErrMissingPrimaryKey
	}
	args := make([]any, 0, len(setColumns)+len(pkColumns))
	sets := make([]string, 0, len(setColumns))
	index := 1
	for _, column := range setColumns {
		quoted, err := w.dialect.quoteColumn(column.name)
		if err != nil {
			return "", nil, err
		}
		sets = append(sets, fmt.Sprintf("%s = %s", quoted, w.dialect.placeholder(index)))
		args = append(args, column.value)
		index++
	}
	wheres := make([]string, 0, len(pkColumns))
	for _, column := range pkColumns {
		quoted, err := w.dialect.quoteColumn(column.name)
		if err != nil {
			return "", nil, err
		}
		wheres = append(wheres, fmt.Sprintf("%s = %s", quoted, w.dialect.placeholder(index)))
		args = append(args, column.value)
		index++
	}
	query := fmt.Sprintf("UPDATE %s SET %s WHERE %s",
		tableName,
		strings.Join(sets, ", "),
		strings.Join(wheres, " AND "),
	)
	return query, args, nil
}

func (w *Writer) buildUpsertSQL(table string, columns []columnValue, conflictColumns, updateColumns []string, doNothing bool) (string, []any, error) {
	insertQuery, args, err := w.buildInsertSQL(table, columns)
	if err != nil {
		return "", nil, err
	}
	if doNothing {
		return w.buildDoNothingSQL(insertQuery, columns, conflictColumns), args, nil
	}
	if len(updateColumns) == 0 {
		return w.buildDoNothingSQL(insertQuery, columns, conflictColumns), args, nil
	}
	updates := make([]string, 0, len(updateColumns))
	for _, column := range updateColumns {
		quoted, err := w.dialect.quoteColumn(column)
		if err != nil {
			return "", nil, err
		}
		switch w.dialect {
		case MySQL:
			updates = append(updates, fmt.Sprintf("%s = VALUES(%s)", quoted, quoted))
		default:
			updates = append(updates, fmt.Sprintf("%s = excluded.%s", quoted, quoted))
		}
	}
	if w.dialect == MySQL {
		return insertQuery + " ON DUPLICATE KEY UPDATE " + strings.Join(updates, ", "), args, nil
	}
	conflict, err := w.quotedConflictColumns(conflictColumns)
	if err != nil {
		return "", nil, err
	}
	return insertQuery + " ON CONFLICT (" + strings.Join(conflict, ", ") + ") DO UPDATE SET " + strings.Join(updates, ", "), args, nil
}

func (w *Writer) buildDoNothingSQL(insertQuery string, columns []columnValue, conflictColumns []string) string {
	if w.dialect == MySQL {
		target := columns[0].name
		if len(conflictColumns) > 0 {
			target = conflictColumns[0]
		}
		quoted, err := w.dialect.quoteColumn(target)
		if err != nil {
			return insertQuery
		}
		return insertQuery + " ON DUPLICATE KEY UPDATE " + quoted + " = " + quoted
	}
	conflict, err := w.quotedConflictColumns(conflictColumns)
	if err != nil || len(conflict) == 0 {
		return insertQuery + " ON CONFLICT DO NOTHING"
	}
	return insertQuery + " ON CONFLICT (" + strings.Join(conflict, ", ") + ") DO NOTHING"
}

func (w *Writer) buildBatchInsertSQL(table string, columns []columnValue, rows [][]columnValue) (string, []any, error) {
	tableName, err := w.dialect.quoteTable(table)
	if err != nil {
		return "", nil, err
	}
	columnNames, err := w.quotedColumnNames(columns)
	if err != nil {
		return "", nil, err
	}
	args := make([]any, 0, len(rows)*len(columns))
	valueGroups := make([]string, 0, len(rows))
	argIndex := 1
	for _, row := range rows {
		placeholders := make([]string, 0, len(columns))
		for _, column := range row {
			placeholders = append(placeholders, w.dialect.placeholder(argIndex))
			args = append(args, column.value)
			argIndex++
		}
		valueGroups = append(valueGroups, "("+strings.Join(placeholders, ", ")+")")
	}
	query := fmt.Sprintf("INSERT INTO %s (%s) VALUES %s",
		tableName,
		strings.Join(columnNames, ", "),
		strings.Join(valueGroups, ", "),
	)
	return query, args, nil
}

func (w *Writer) quotedColumnNames(columns []columnValue) ([]string, error) {
	names := make([]string, 0, len(columns))
	for _, column := range columns {
		quoted, err := w.dialect.quoteColumn(column.name)
		if err != nil {
			return nil, err
		}
		names = append(names, quoted)
	}
	return names, nil
}

func (w *Writer) quotedConflictColumns(columns []string) ([]string, error) {
	quotedColumns := make([]string, 0, len(columns))
	for _, column := range columns {
		quoted, err := w.dialect.quoteColumn(column)
		if err != nil {
			return nil, err
		}
		quotedColumns = append(quotedColumns, quoted)
	}
	return quotedColumns, nil
}

func (w *Writer) writeContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if w.writeTimeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, w.writeTimeout)
}

func insertableColumns(columns []columnValue) []columnValue {
	insertable := make([]columnValue, 0, len(columns))
	for _, column := range columns {
		if column.auto {
			continue
		}
		insertable = append(insertable, column)
	}
	return insertable
}

func splitUpdateColumns(columns []columnValue) ([]columnValue, []columnValue) {
	setColumns := make([]columnValue, 0, len(columns))
	pkColumns := make([]columnValue, 0, 1)
	for _, column := range columns {
		if column.primary {
			pkColumns = append(pkColumns, column)
			continue
		}
		if column.auto {
			continue
		}
		setColumns = append(setColumns, column)
	}
	return setColumns, pkColumns
}

func alignColumns(schema []columnValue, columns []columnValue) []columnValue {
	byName := make(map[string]columnValue, len(columns))
	for _, column := range columns {
		byName[column.name] = column
	}
	row := make([]columnValue, 0, len(schema))
	for _, column := range schema {
		value := byName[column.name]
		value.name = column.name
		row = append(row, value)
	}
	return row
}

func containsName(names []string, name string) bool {
	for _, candidate := range names {
		if candidate == name {
			return true
		}
	}
	return false
}
