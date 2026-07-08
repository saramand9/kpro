package gormstore

import (
	"context"
	"errors"
	"testing"

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
