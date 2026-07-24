package gormstore

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	_ "modernc.org/sqlite"
)

func TestOpenDoesNotCloseCallerOwnedDatabase(t *testing.T) {
	native, sqlDB := openNativeGORM(t)
	defer sqlDB.Close()

	database, err := Open(context.Background(), Config{DB: native})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.PingContext(context.Background()); err != nil {
		t.Fatalf("caller-owned pool was closed: %v", err)
	}
}

func TestWrapDoesNotCloseCallerOwnedDatabase(t *testing.T) {
	native, sqlDB := openNativeGORM(t)
	defer sqlDB.Close()

	if err := Wrap("external", native).Close(); err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.PingContext(context.Background()); err != nil {
		t.Fatalf("wrapped caller-owned pool was closed: %v", err)
	}
}

func TestOpenPingFailureDoesNotCloseCallerOwnedDatabase(t *testing.T) {
	native, sqlDB := openNativeGORM(t)
	defer sqlDB.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Open(ctx, Config{DB: native}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Open error=%v, want context.Canceled", err)
	}
	if err := sqlDB.PingContext(context.Background()); err != nil {
		t.Fatalf("ping failure closed caller-owned pool: %v", err)
	}
}

func TestOpenClosesDatabaseCreatedFromDialector(t *testing.T) {
	database, err := Open(context.Background(), Config{
		Dialector: sqlite.Dialector{DriverName: "sqlite", DSN: ":memory:"},
	})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := database.SQLDB()
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.PingContext(context.Background()); err == nil {
		t.Fatal("owned pool remained open")
	}
}

func openNativeGORM(t *testing.T) (*gorm.DB, *sql.DB) {
	t.Helper()
	native, err := gorm.Open(sqlite.Dialector{DriverName: "sqlite", DSN: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := native.DB()
	if err != nil {
		t.Fatal(err)
	}
	return native, sqlDB
}
