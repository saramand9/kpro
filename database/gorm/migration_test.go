package gormstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"gorm.io/driver/sqlite"

	_ "modernc.org/sqlite"
)

func TestApplyMigrationsRunsOnceInVersionOrder(t *testing.T) {
	database := openMigrationTestDatabase(t)
	defer closeMigrationTestDatabase(t, database)

	migrations := []Migration{
		{
			Version: 2,
			Name:    "seed_row",
			Statements: []Statement{
				SQL("INSERT INTO migration_test_items (id, name) VALUES (1, 'first')"),
			},
		},
		{
			Version: 1,
			Name:    "create_items",
			Statements: []Statement{
				SQL("CREATE TABLE migration_test_items (id INTEGER PRIMARY KEY, name TEXT NOT NULL)"),
			},
		},
	}

	if err := ApplyMigrations(context.Background(), database, migrations); err != nil {
		t.Fatal(err)
	}
	if err := ApplyMigrations(context.Background(), database, migrations); err != nil {
		t.Fatal(err)
	}

	var count int64
	if err := database.GORM().Raw("SELECT COUNT(1) FROM migration_test_items").Scan(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("row count = %d, want 1", count)
	}

	applied, err := AppliedMigrations(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 2 || applied[0].Version != 1 || applied[1].Version != 2 {
		t.Fatalf("applied migrations = %#v", applied)
	}
	for _, migration := range applied {
		if migration.Status != migrationStatusApplied {
			t.Fatalf("migration %d status = %q", migration.Version, migration.Status)
		}
		if migration.Checksum == "" {
			t.Fatalf("migration %d missing checksum", migration.Version)
		}
		if migration.AppliedAt == nil || migration.AppliedAt.IsZero() {
			t.Fatalf("migration %d missing applied_at", migration.Version)
		}
	}
}

func TestApplyMigrationsRejectsDuplicateVersions(t *testing.T) {
	database := openMigrationTestDatabase(t)
	defer closeMigrationTestDatabase(t, database)

	err := ApplyMigrations(context.Background(), database, []Migration{
		{Version: 1, Name: "one", Statements: []Statement{SQL("SELECT 1")}},
		{Version: 1, Name: "duplicate", Statements: []Statement{SQL("SELECT 1")}},
	})
	if !errors.Is(err, ErrInvalidMigration) {
		t.Fatalf("err = %v, want ErrInvalidMigration", err)
	}
}

func TestApplyMigrationsRejectsUnsupportedDialect(t *testing.T) {
	database := openMigrationTestDatabase(t)
	defer closeMigrationTestDatabase(t, database)

	err := ApplyMigrations(context.Background(), database, []Migration{
		{
			Version: 1,
			Name:    "missing_sql",
			Statements: []Statement{
				DialectSQL("", map[string]string{"mysql": "SELECT 1"}),
			},
		},
	})
	if !errors.Is(err, ErrUnsupportedDialect) {
		t.Fatalf("err = %v, want ErrUnsupportedDialect", err)
	}
}

func TestApplyMigrationsBackfillsLegacyRows(t *testing.T) {
	database := openMigrationTestDatabase(t)
	defer closeMigrationTestDatabase(t, database)

	if err := database.GORM().Exec(`CREATE TABLE schema_migrations (
version BIGINT NOT NULL PRIMARY KEY,
name VARCHAR(255) NOT NULL,
applied_at TIMESTAMP NOT NULL
)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.GORM().Exec("INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)", 1, "legacy", time.Now().UTC()).Error; err != nil {
		t.Fatal(err)
	}

	migration := []Migration{{Version: 1, Name: "legacy", Statements: []Statement{SQL("CREATE TABLE legacy_items (id INTEGER PRIMARY KEY)")}}}
	if err := ApplyMigrations(context.Background(), database, migration); err != nil {
		t.Fatal(err)
	}
	applied, err := AppliedMigrations(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 || applied[0].Status != migrationStatusApplied || applied[0].Checksum == "" {
		t.Fatalf("applied migrations = %#v", applied)
	}
}
func TestApplyMigrationsRejectsChangedAppliedMigration(t *testing.T) {
	database := openMigrationTestDatabase(t)
	defer closeMigrationTestDatabase(t, database)

	first := []Migration{{Version: 1, Name: "stable", Statements: []Statement{SQL("CREATE TABLE stable_items (id INTEGER PRIMARY KEY)")}}}
	if err := ApplyMigrations(context.Background(), database, first); err != nil {
		t.Fatal(err)
	}

	changed := []Migration{{Version: 1, Name: "stable", Statements: []Statement{SQL("CREATE TABLE changed_items (id INTEGER PRIMARY KEY)")}}}
	err := ApplyMigrations(context.Background(), database, changed)
	if !errors.Is(err, ErrMigrationChecksum) {
		t.Fatalf("err = %v, want ErrMigrationChecksum", err)
	}
}

func TestApplyMigrationsBlocksFailedMigration(t *testing.T) {
	database := openMigrationTestDatabase(t)
	defer closeMigrationTestDatabase(t, database)

	broken := []Migration{{Version: 1, Name: "broken", Statements: []Statement{SQL("CREATE TABLE broken_items (")}}}
	if err := ApplyMigrations(context.Background(), database, broken); err == nil {
		t.Fatal("expected broken migration error")
	}

	fixed := []Migration{{Version: 1, Name: "broken", Statements: []Statement{SQL("CREATE TABLE broken_items (id INTEGER PRIMARY KEY)")}}}
	err := ApplyMigrations(context.Background(), database, fixed)
	if !errors.Is(err, ErrDirtyMigration) {
		t.Fatalf("err = %v, want ErrDirtyMigration", err)
	}

	applied, err := AppliedMigrations(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 || applied[0].Status != migrationStatusFailed || applied[0].Error == "" {
		t.Fatalf("applied migrations = %#v", applied)
	}
}

func openMigrationTestDatabase(t *testing.T) *Database {
	t.Helper()
	database, err := Open(context.Background(), Config{
		Dialector:    sqlite.Dialector{DriverName: "sqlite", DSN: ":memory:"},
		MaxOpenConns: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return database
}

func closeMigrationTestDatabase(t *testing.T, database *Database) {
	t.Helper()
	if err := database.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}
