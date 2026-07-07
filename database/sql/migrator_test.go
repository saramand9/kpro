package sqlstore

import (
	"context"
	"testing"
)

func TestMigratorAppliesMigrationOnce(t *testing.T) {
	ctx := context.Background()
	database := openOutboxTestDatabase(t)
	migrator := NewMigrator(database, "")
	migration := Migration{
		ID: "test_001_create_items",
		Statements: []string{
			`CREATE TABLE migration_items (id INTEGER PRIMARY KEY, name TEXT NOT NULL)`,
			`INSERT INTO migration_items (id, name) VALUES (1, 'one')`,
		},
	}

	if err := migrator.Apply(ctx, migration); err != nil {
		t.Fatal(err)
	}
	if err := migrator.Apply(ctx, migration); err != nil {
		t.Fatal(err)
	}

	var count int
	if err := database.DB().QueryRowContext(ctx, `SELECT count(*) FROM migration_items`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
	applied, err := migrator.Applied(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 || applied[0] != migration.ID {
		t.Fatalf("applied = %#v, want [%q]", applied, migration.ID)
	}
}

func TestMigratorDoesNotRecordFailedMigration(t *testing.T) {
	ctx := context.Background()
	database := openOutboxTestDatabase(t)
	migrator := NewMigrator(database, "")
	migration := Migration{
		ID: "test_002_failure",
		Statements: []string{
			`CREATE TABLE migration_failure (id INTEGER PRIMARY KEY)`,
			`INSERT INTO missing_table (id) VALUES (1)`,
		},
	}

	if err := migrator.Apply(ctx, migration); err == nil {
		t.Fatal("expected migration failure")
	}
	applied, err := migrator.Applied(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range applied {
		if id == migration.ID {
			t.Fatalf("failed migration %q was recorded", migration.ID)
		}
	}
	if err := migrator.Apply(ctx, migration); err == nil {
		t.Fatal("expected failed migration to require intervention")
	}
}

func TestMigratorRejectsChecksumMismatch(t *testing.T) {
	ctx := context.Background()
	database := openOutboxTestDatabase(t)
	migrator := NewMigrator(database, "")

	first := Migration{
		ID:         "test_003_checksum",
		Statements: []string{`CREATE TABLE migration_checksum (id INTEGER PRIMARY KEY)`},
	}
	if err := migrator.Apply(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := Migration{
		ID:         first.ID,
		Statements: []string{`CREATE TABLE migration_checksum_changed (id INTEGER PRIMARY KEY)`},
	}
	if err := migrator.Apply(ctx, second); err == nil {
		t.Fatal("expected checksum mismatch")
	}
}

func TestMigratorUpgradesLegacyMetadataTable(t *testing.T) {
	ctx := context.Background()
	database := openOutboxTestDatabase(t)
	if _, err := database.DB().ExecContext(ctx, `CREATE TABLE schema_migrations (id TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB().ExecContext(ctx, `INSERT INTO schema_migrations (id, applied_at) VALUES ('legacy_001', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}

	migrator := NewMigrator(database, "")
	migration := Migration{
		ID:         "legacy_001",
		Statements: []string{`CREATE TABLE legacy_should_not_execute (id INTEGER PRIMARY KEY)`},
	}
	if err := migrator.Apply(ctx, migration); err != nil {
		t.Fatal(err)
	}
	state, ok, err := migrator.state(ctx, migration.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected legacy migration state")
	}
	if state.status != migrationStatusApplied || state.checksum == "" {
		t.Fatalf("legacy state = %#v, want applied with checksum", state)
	}
}
