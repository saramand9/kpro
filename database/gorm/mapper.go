package gormstore

import (
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"unicode"
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

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
	if value.Kind() != reflect.Struct {
		return nil, fmt.Errorf("%w: unsupported record %T", ErrNoColumns, record)
	}
	return mapStructRecord(value, includeOmitEmpty)
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
		if err := validateColumnName(column.name); err != nil {
			return nil, err
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

func primaryColumn(columns []columnValue) columnValue {
	for _, column := range columns {
		if column.primary {
			return column
		}
	}
	return columnValue{}
}

func columnNames(columns []columnValue) []string {
	names := make([]string, 0, len(columns))
	for _, column := range columns {
		names = append(names, column.name)
	}
	return names
}

func columnMap(columns []columnValue) map[string]any {
	values := make(map[string]any, len(columns))
	for _, column := range columns {
		values[column.name] = column.value
	}
	return values
}

func containsName(names []string, name string) bool {
	for _, candidate := range names {
		if candidate == name {
			return true
		}
	}
	return false
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

func validateTableName(name string) error {
	parts := strings.Split(name, ".")
	if len(parts) == 0 {
		return fmt.Errorf("%w: empty table", ErrInvalidIdentifier)
	}
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if !identifierPattern.MatchString(part) {
			return fmt.Errorf("%w: table %q", ErrInvalidIdentifier, name)
		}
	}
	return nil
}

func validateColumnName(name string) error {
	name = strings.TrimSpace(name)
	if !identifierPattern.MatchString(name) {
		return fmt.Errorf("%w: column %q", ErrInvalidIdentifier, name)
	}
	return nil
}

func validateColumnNames(names ...string) error {
	for _, name := range names {
		if err := validateColumnName(name); err != nil {
			return err
		}
	}
	return nil
}
