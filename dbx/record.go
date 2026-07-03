// File: dbx/record.go
// Purpose: Maps Go values to database columns for generated writes.

package dbx

import (
	"fmt"
	"reflect"
	"strings"
	"unicode"
)

type columnValue struct {
	name      string
	value     any
	primary   bool
	auto      bool
	omitempty bool
}

func mapRecord(record any) ([]columnValue, error) {
	return mapRecordWithOptions(record, false)
}

func mapRecordSchema(record any) ([]columnValue, error) {
	return mapRecordWithOptions(record, true)
}

func mapRecordWithOptions(record any, includeOmitEmpty bool) ([]columnValue, error) {
	if record == nil {
		return nil, fmt.Errorf("%w: nil record", ErrNoColumns)
	}
	value := reflectValue(record)
	if !value.IsValid() {
		return nil, fmt.Errorf("%w: nil record", ErrNoColumns)
	}
	switch value.Kind() {
	case reflect.Struct:
		return mapStructRecord(value, includeOmitEmpty)
	case reflect.Map:
		return mapMapRecord(value)
	default:
		return nil, fmt.Errorf("%w: unsupported record %T", ErrNoColumns, record)
	}
}

func reflectValue(input any) reflect.Value {
	value := reflect.ValueOf(input)
	for value.IsValid() && value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return reflect.Value{}
		}
		value = value.Elem()
	}
	return value
}
func mapStructRecord(value reflect.Value, includeOmitEmpty bool) ([]columnValue, error) {
	valueType := value.Type()
	columns := make([]columnValue, 0, value.NumField())
	for i := 0; i < value.NumField(); i++ {
		field := valueType.Field(i)
		if field.PkgPath != "" {
			continue
		}
		column, ok := parseDBTag(field)
		if !ok {
			continue
		}
		fieldValue := value.Field(i)
		column.value = fieldValue.Interface()
		if column.name == "" {
			column.name = toSnakeCase(field.Name)
		}
		if !column.primary && strings.EqualFold(field.Name, "id") {
			column.primary = true
		}
		if column.omitempty && fieldValue.IsZero() && !includeOmitEmpty {
			continue
		}
		columns = append(columns, column)
	}
	if len(columns) == 0 {
		return nil, ErrNoColumns
	}
	return columns, nil
}

func parseDBTag(field reflect.StructField) (columnValue, bool) {
	tag := field.Tag.Get("db")
	if tag == "-" {
		return columnValue{}, false
	}
	column := columnValue{}
	if tag == "" {
		return column, true
	}
	parts := strings.Split(tag, ",")
	column.name = strings.TrimSpace(parts[0])
	for _, option := range parts[1:] {
		switch strings.TrimSpace(option) {
		case "pk":
			column.primary = true
		case "auto":
			column.auto = true
		case "omitempty":
			column.omitempty = true
		}
	}
	return column, true
}

func mapMapRecord(value reflect.Value) ([]columnValue, error) {
	if value.Type().Key().Kind() != reflect.String {
		return nil, fmt.Errorf("%w: map key must be string", ErrNoColumns)
	}
	columns := make([]columnValue, 0, value.Len())
	iter := value.MapRange()
	for iter.Next() {
		name := iter.Key().String()
		columns = append(columns, columnValue{
			name:    name,
			value:   iter.Value().Interface(),
			primary: strings.EqualFold(name, "id"),
		})
	}
	if len(columns) == 0 {
		return nil, ErrNoColumns
	}
	sortColumns(columns)
	return columns, nil
}

func toSnakeCase(name string) string {
	var builder strings.Builder
	for i, r := range name {
		if unicode.IsUpper(r) {
			if i > 0 {
				builder.WriteByte('_')
			}
			builder.WriteRune(unicode.ToLower(r))
			continue
		}
		builder.WriteRune(r)
	}
	return builder.String()
}

func sortColumns(columns []columnValue) {
	for i := 1; i < len(columns); i++ {
		for j := i; j > 0 && columns[j-1].name > columns[j].name; j-- {
			columns[j-1], columns[j] = columns[j], columns[j-1]
		}
	}
}
