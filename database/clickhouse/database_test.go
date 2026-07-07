package clickhousestore

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"testing"

	"github.com/saramand9/bignet/kpro/database/common/lifecycle"
)

func init() {
	sql.Register("clickhouse_store_fake", fakeDriver{})
}

type fakeDriver struct{}

func (fakeDriver) Open(name string) (driver.Conn, error) {
	return fakeConn{}, nil
}

type fakeConn struct{}

func (fakeConn) Prepare(query string) (driver.Stmt, error) {
	return nil, errors.New("prepare not implemented")
}
func (fakeConn) Close() error                   { return nil }
func (fakeConn) Begin() (driver.Tx, error)      { return fakeTx{}, nil }
func (fakeConn) Ping(ctx context.Context) error { return nil }

func (fakeConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	return fakeResult(1), nil
}

func (fakeConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return &fakeRows{sent: false}, nil
}

type fakeTx struct{}

func (fakeTx) Commit() error   { return nil }
func (fakeTx) Rollback() error { return nil }

type fakeResult int64

func (r fakeResult) LastInsertId() (int64, error) { return 0, nil }
func (r fakeResult) RowsAffected() (int64, error) { return int64(r), nil }

type fakeRows struct {
	sent bool
}

func (r *fakeRows) Columns() []string { return []string{"value"} }
func (r *fakeRows) Close() error      { return nil }

func (r *fakeRows) Next(dest []driver.Value) error {
	if r.sent {
		return io.EOF
	}
	r.sent = true
	dest[0] = int64(1)
	return nil
}

func TestOpenExecQueryTxAndHealth(t *testing.T) {
	database, err := Open(context.Background(), Config{
		Name:         "events",
		DriverName:   "clickhouse_store_fake",
		DSN:          "fake",
		MaxOpenConns: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	if database.Name() != "events" || database.Kind() != lifecycle.KindAnalytics {
		t.Fatalf("resource=%s/%s", database.Name(), database.Kind())
	}
	if database.Stats().MaxOpenConnections != 2 {
		t.Fatalf("max open conns=%d, want 2", database.Stats().MaxOpenConnections)
	}
	if health := database.Health(context.Background()); health.Status != lifecycle.StatusOK {
		t.Fatalf("health=%#v", health)
	}
	result, err := database.Exec(context.Background(), "INSERT INTO events VALUES (?)", 1)
	if err != nil {
		t.Fatal(err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil || rowsAffected != 1 {
		t.Fatalf("rows affected=%d err=%v", rowsAffected, err)
	}
	rows, err := database.Query(context.Background(), "SELECT 1")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("query returned no rows")
	}
	if err := database.Tx(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), "INSERT INTO events VALUES (?)", 1)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestOpenValidatesConfig(t *testing.T) {
	if _, err := Open(context.Background(), Config{}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("missing config err=%v, want ErrInvalidConfig", err)
	}
	db, err := sql.Open("clickhouse_store_fake", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = Open(context.Background(), Config{DB: db, DSN: "clickhouse://example"})
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("mixed config err=%v, want ErrInvalidConfig", err)
	}
}

func TestAnalyticsTableWrites(t *testing.T) {
	database, err := Open(context.Background(), Config{
		DriverName: "clickhouse_store_fake",
		DSN:        "fake",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	type event struct {
		Name string
	}
	writes := 0
	table := NewAnalyticsTable[event](database, "events", func(ctx context.Context, db *sql.DB, value event) error {
		writes++
		_, err := db.ExecContext(ctx, "INSERT INTO events VALUES (?)", value.Name)
		return err
	}, nil)
	if table.Name() != "events" {
		t.Fatalf("table name=%q", table.Name())
	}
	if err := table.Write(context.Background(), event{Name: "one"}); err != nil {
		t.Fatal(err)
	}
	if err := table.WriteBatch(context.Background(), []event{{Name: "two"}, {Name: "three"}}); err != nil {
		t.Fatal(err)
	}
	if writes != 3 {
		t.Fatalf("writes=%d, want 3", writes)
	}
}
