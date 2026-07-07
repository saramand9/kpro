package gormstore

import (
	"database/sql"
	"reflect"
)

type result struct {
	rowsAffected    int64
	lastInsertID    int64
	hasLastInsertID bool
}

var _ sql.Result = result{}

func (r result) LastInsertId() (int64, error) {
	if !r.hasLastInsertID {
		return 0, ErrLastInsertIDUnavailable
	}
	return r.lastInsertID, nil
}

func (r result) RowsAffected() (int64, error) {
	return r.rowsAffected, nil
}

func newResult(rowsAffected int64, record any) sql.Result {
	lastInsertID, ok := primaryInt64(record)
	return result{
		rowsAffected:    rowsAffected,
		lastInsertID:    lastInsertID,
		hasLastInsertID: ok,
	}
}

func rowsResult(rowsAffected int64) sql.Result {
	return result{rowsAffected: rowsAffected}
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
