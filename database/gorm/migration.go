// File: database/gorm/migration.go
// Purpose: Provides versioned schema migrations for GORM-backed resources.

package gormstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/saramand9/bignet/kpro/database/common/lifecycle"
	"gorm.io/gorm"
)

const (
	schemaMigrationsTable = "schema_migrations"
	migrationLockName     = "kpro_schema_migrations"
	postgresLockKey       = int64(742607080001)
	migrationLockTimeout  = 30

	migrationStatusDirty   = "dirty"
	migrationStatusFailed  = "failed"
	migrationStatusApplied = "applied"
)

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
	Checksum  string
	Status    string
	StartedAt time.Time
	AppliedAt *time.Time
	Error     string
}

type schemaMigrationRow struct {
	Version   int64      `gorm:"column:version;primaryKey"`
	Name      string     `gorm:"column:name;size:255"`
	Checksum  string     `gorm:"column:checksum;size:64"`
	Status    string     `gorm:"column:status;size:32"`
	StartedAt time.Time  `gorm:"column:started_at"`
	AppliedAt *time.Time `gorm:"column:applied_at"`
	Error     string     `gorm:"column:error;type:text"`
}

func (schemaMigrationRow) TableName() string {
	return schemaMigrationsTable
}

type resolvedMigration struct {
	Version  int64
	Name     string
	SQLs     []string
	Checksum string
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
	db = db.WithContext(contextOrBackground(ctx))
	resolved, err := resolveMigrations(normalized, db.Dialector.Name())
	if err != nil {
		return err
	}
	return withMigrationLock(ctx, db, func(locked *gorm.DB) error {
		if err := ensureMigrationsTable(locked); err != nil {
			return err
		}
		for _, migration := range resolved {
			if err := applyResolvedMigration(locked, migration); err != nil {
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
	db = db.WithContext(contextOrBackground(ctx))
	if err := ensureMigrationsTable(db); err != nil {
		return nil, err
	}
	var rows []schemaMigrationRow
	if err := db.Order("version ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	migrations := make([]AppliedMigration, 0, len(rows))
	for _, row := range rows {
		migrations = append(migrations, AppliedMigration{
			Version:   row.Version,
			Name:      row.Name,
			Checksum:  row.Checksum,
			Status:    row.Status,
			StartedAt: row.StartedAt,
			AppliedAt: row.AppliedAt,
			Error:     row.Error,
		})
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

func resolveMigrations(migrations []Migration, dialect string) ([]resolvedMigration, error) {
	resolved := make([]resolvedMigration, 0, len(migrations))
	for _, migration := range migrations {
		sqls := make([]string, 0, len(migration.Statements))
		for _, statement := range migration.Statements {
			sql, err := statement.sqlFor(dialect)
			if err != nil {
				return nil, fmt.Errorf("%w: %s migration %d", err, dialect, migration.Version)
			}
			if strings.TrimSpace(sql) != "" {
				sqls = append(sqls, sql)
			}
		}
		if len(sqls) == 0 {
			return nil, fmt.Errorf("%w: migration %d has no executable statements", ErrInvalidMigration, migration.Version)
		}
		resolved = append(resolved, resolvedMigration{
			Version:  migration.Version,
			Name:     strings.TrimSpace(migration.Name),
			SQLs:     sqls,
			Checksum: migrationChecksum(migration.Version, strings.TrimSpace(migration.Name), sqls),
		})
	}
	return resolved, nil
}

func withMigrationLock(ctx context.Context, db *gorm.DB, fn func(*gorm.DB) error) error {
	switch db.Dialector.Name() {
	case "mysql":
		var acquired int
		if err := db.Raw("SELECT GET_LOCK(?, ?)", migrationLockName, migrationLockTimeout).Scan(&acquired).Error; err != nil {
			return err
		}
		if acquired != 1 {
			return ErrMigrationLock
		}
		defer db.Exec("SELECT RELEASE_LOCK(?)", migrationLockName)
	case "postgres":
		if err := db.Exec("SELECT pg_advisory_lock(?)", postgresLockKey).Error; err != nil {
			return err
		}
		defer db.Exec("SELECT pg_advisory_unlock(?)", postgresLockKey)
	}
	return fn(db.WithContext(contextOrBackground(ctx)))
}

func ensureMigrationsTable(db *gorm.DB) error {
	return db.AutoMigrate(&schemaMigrationRow{})
}

func applyResolvedMigration(db *gorm.DB, migration resolvedMigration) error {
	existing, err := findMigration(db, migration.Version)
	if err != nil {
		return err
	}
	if existing != nil {
		if isLegacyAppliedMigration(*existing) {
			return backfillLegacyMigration(db, migration)
		}
		return validateExistingMigration(*existing, migration)
	}
	started := time.Now().UTC()
	row := schemaMigrationRow{
		Version:   migration.Version,
		Name:      migration.Name,
		Checksum:  migration.Checksum,
		Status:    migrationStatusDirty,
		StartedAt: started,
	}
	if err := db.Create(&row).Error; err != nil {
		return err
	}
	for _, sql := range migration.SQLs {
		if err := db.Exec(sql).Error; err != nil {
			wrapped := fmt.Errorf("apply migration %d %s: %w", migration.Version, migration.Name, err)
			_ = markMigrationFailed(db, migration.Version, wrapped)
			return wrapped
		}
	}
	appliedAt := time.Now().UTC()
	return db.Model(&schemaMigrationRow{}).
		Where("version = ?", migration.Version).
		Updates(map[string]any{
			"status":     migrationStatusApplied,
			"applied_at": appliedAt,
			"error":      "",
		}).Error
}

func findMigration(db *gorm.DB, version int64) (*schemaMigrationRow, error) {
	var row schemaMigrationRow
	err := db.First(&row, "version = ?", version).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func isLegacyAppliedMigration(row schemaMigrationRow) bool {
	return row.Status == "" && row.Checksum == ""
}

func backfillLegacyMigration(db *gorm.DB, migration resolvedMigration) error {
	appliedAt := time.Now().UTC()
	return db.Model(&schemaMigrationRow{}).
		Where("version = ?", migration.Version).
		Updates(map[string]any{
			"name":       migration.Name,
			"checksum":   migration.Checksum,
			"status":     migrationStatusApplied,
			"started_at": appliedAt,
			"applied_at": appliedAt,
			"error":      "",
		}).Error
}
func validateExistingMigration(row schemaMigrationRow, migration resolvedMigration) error {
	if row.Status != migrationStatusApplied {
		return fmt.Errorf("%w: migration %d status %s", ErrDirtyMigration, row.Version, row.Status)
	}
	if row.Checksum != "" && row.Checksum != migration.Checksum {
		return fmt.Errorf("%w: migration %d", ErrMigrationChecksum, row.Version)
	}
	return nil
}

func markMigrationFailed(db *gorm.DB, version int64, migrationErr error) error {
	return db.Model(&schemaMigrationRow{}).
		Where("version = ?", version).
		Updates(map[string]any{
			"status": migrationStatusFailed,
			"error":  migrationErr.Error(),
		}).Error
}

func migrationChecksum(version int64, name string, sqls []string) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%d\n%s\n", version, name)
	for _, sql := range sqls {
		_, _ = h.Write([]byte(strings.TrimSpace(sql)))
		_, _ = h.Write([]byte("\n---\n"))
	}
	return hex.EncodeToString(h.Sum(nil))
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

func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
