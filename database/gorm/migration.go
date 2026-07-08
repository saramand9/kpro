// File: database/gorm/migration.go
// Purpose: Provides versioned schema migrations for GORM-backed resources.

package gormstore

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/saramand9/bignet/kpro/database/common/lifecycle"
	"gorm.io/gorm"
)

const schemaMigrationsTable = "schema_migrations"

// Statement is one SQL statement in a versioned migration.
type Statement struct {
	Default  string
	Dialects map[string]string
}

// SQL creates a migration statement for all dialects.
func SQL(statement string) Statement {
	return Statement{Default: statement}
}

// DialectSQL creates a migration statement with dialect-specific overrides.
func DialectSQL(defaultSQL string, dialects map[string]string) Statement {
	return Statement{Default: defaultSQL, Dialects: dialects}
}

// Migration is a versioned schema change.
type Migration struct {
	Version    int64
	Name       string
	Statements []Statement
}

// AppliedMigration is one row from schema_migrations.
type AppliedMigration struct {
	Version   int64
	Name      string
	AppliedAt time.Time
}

// ApplyMigrations runs unapplied migrations in version order.
func ApplyMigrations(ctx context.Context, database *Database, migrations []Migration) error {
	db, err := gormDB(database)
	if err != nil {
		return err
	}
	if len(migrations) == 0 {
		return nil
	}
	normalized, err := normalizeMigrations(migrations)
	if err != nil {
		return err
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := ensureMigrationsTable(tx); err != nil {
			return err
		}
		dialect := tx.Dialector.Name()
		for _, migration := range normalized {
			applied, err := migrationApplied(tx, migration.Version)
			if err != nil {
				return err
			}
			if applied {
				continue
			}
			for _, statement := range migration.Statements {
				sql, err := statement.sqlFor(dialect)
				if err != nil {
					return fmt.Errorf("%w: %s migration %d", err, dialect, migration.Version)
				}
				if strings.TrimSpace(sql) == "" {
					continue
				}
				if err := tx.Exec(sql).Error; err != nil {
					return fmt.Errorf("apply migration %d %s: %w", migration.Version, migration.Name, err)
				}
			}
			if err := insertMigration(tx, migration); err != nil {
				return err
			}
		}
		return nil
	})
}

// AppliedMigrations returns applied migration records in version order.
func AppliedMigrations(ctx context.Context, database *Database) ([]AppliedMigration, error) {
	db, err := gormDB(database)
	if err != nil {
		return nil, err
	}
	var migrations []AppliedMigration
	if err := ensureMigrationsTable(db.WithContext(ctx)); err != nil {
		return nil, err
	}
	if err := db.WithContext(ctx).
		Raw("SELECT version, name, applied_at FROM " + schemaMigrationsTable + " ORDER BY version ASC").
		Scan(&migrations).Error; err != nil {
		return nil, err
	}
	return migrations, nil
}

func gormDB(database *Database) (*gorm.DB, error) {
	if database == nil || database.GORM() == nil {
		return nil, fmt.Errorf("%w: gorm database is nil", lifecycle.ErrUnavailable)
	}
	return database.GORM(), nil
}

func normalizeMigrations(migrations []Migration) ([]Migration, error) {
	normalized := append([]Migration(nil), migrations...)
	sort.Slice(normalized, func(i, j int) bool {
		return normalized[i].Version < normalized[j].Version
	})
	seen := make(map[int64]struct{}, len(normalized))
	for _, migration := range normalized {
		if migration.Version <= 0 {
			return nil, fmt.Errorf("%w: version must be positive", ErrInvalidMigration)
		}
		if strings.TrimSpace(migration.Name) == "" {
			return nil, fmt.Errorf("%w: migration %d name is required", ErrInvalidMigration, migration.Version)
		}
		if _, ok := seen[migration.Version]; ok {
			return nil, fmt.Errorf("%w: duplicate version %d", ErrInvalidMigration, migration.Version)
		}
		seen[migration.Version] = struct{}{}
		if len(migration.Statements) == 0 {
			return nil, fmt.Errorf("%w: migration %d has no statements", ErrInvalidMigration, migration.Version)
		}
	}
	return normalized, nil
}

func ensureMigrationsTable(db *gorm.DB) error {
	return db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
version BIGINT NOT NULL PRIMARY KEY,
name VARCHAR(255) NOT NULL,
applied_at TIMESTAMP NOT NULL
)`).Error
}

func migrationApplied(db *gorm.DB, version int64) (bool, error) {
	var count int64
	if err := db.Raw("SELECT COUNT(1) FROM "+schemaMigrationsTable+" WHERE version = ?", version).Scan(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func insertMigration(db *gorm.DB, migration Migration) error {
	return db.Exec(
		"INSERT INTO "+schemaMigrationsTable+" (version, name, applied_at) VALUES (?, ?, ?)",
		migration.Version,
		migration.Name,
		time.Now().UTC(),
	).Error
}

func (s Statement) sqlFor(dialect string) (string, error) {
	if s.Dialects != nil {
		if sql, ok := s.Dialects[dialect]; ok {
			return sql, nil
		}
	}
	if strings.TrimSpace(s.Default) != "" {
		return s.Default, nil
	}
	return "", ErrUnsupportedDialect
}
