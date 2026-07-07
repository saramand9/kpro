// File: database/dbx/dialect.go
// Purpose: Encapsulates SQL dialect differences for generated writes.

package dbx

import (
	"fmt"
	"regexp"
	"strings"
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func errMissingDriverName() error {
	return fmt.Errorf("%w: driver name is required", ErrInvalidConfig)
}

func inferDialect(driverName string) Dialect {
	switch strings.ToLower(driverName) {
	case "sqlite", "sqlite3":
		return SQLite
	case "mysql":
		return MySQL
	case "postgres", "postgresql", "pgx":
		return Postgres
	default:
		return SQLite
	}
}

func (d Dialect) validate() error {
	switch d {
	case SQLite, MySQL, Postgres:
		return nil
	default:
		return fmt.Errorf("%w: unsupported dialect %q", ErrInvalidIdentifier, d)
	}
}

func (d Dialect) placeholder(index int) string {
	if d == Postgres {
		return fmt.Sprintf("$%d", index)
	}
	return "?"
}

func (d Dialect) quoteTable(name string) (string, error) {
	parts := strings.Split(name, ".")
	if len(parts) == 0 {
		return "", fmt.Errorf("%w: empty table", ErrInvalidIdentifier)
	}
	quoted := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if !identifierPattern.MatchString(part) {
			return "", fmt.Errorf("%w: table %q", ErrInvalidIdentifier, name)
		}
		quoted = append(quoted, d.quoteIdentifier(part))
	}
	return strings.Join(quoted, "."), nil
}

func (d Dialect) quoteColumn(name string) (string, error) {
	name = strings.TrimSpace(name)
	if !identifierPattern.MatchString(name) {
		return "", fmt.Errorf("%w: column %q", ErrInvalidIdentifier, name)
	}
	return d.quoteIdentifier(name), nil
}

func (d Dialect) quoteIdentifier(name string) string {
	if d == MySQL {
		return "`" + name + "`"
	}
	return `"` + name + `"`
}
