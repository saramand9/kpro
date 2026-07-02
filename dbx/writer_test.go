// File: dbx/writer_test.go
// Purpose: Tests database writer SQL generation and execution.

package dbx

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	_ "modernc.org/sqlite"
)

type testUser struct {
	ID      int64  `db:"id,pk,auto"`
	Name    string `db:"name"`
	Age     int    `db:"age"`
	Empty   string `db:"empty,omitempty"`
	Ignored string `db:"-"`
}

type testUpsertUser struct {
	ID   int64  `db:"id,pk"`
	Name string `db:"name"`
	Age  int    `db:"age"`
}

func TestWriterGeneratesPostgresInsert(t *testing.T) {
	writer := NewWriter(nil, Postgres, WriterConfig{})

	query, args, err := writer.insertSQL("public.users", testUser{Name: "Ada", Age: 37})
	if err != nil {
		t.Fatal(err)
	}
	wantQuery := `INSERT INTO "public"."users" ("name", "age") VALUES ($1, $2)`
	if query != wantQuery {
		t.Fatalf("query = %q, want %q", query, wantQuery)
	}
	wantArgs := []any{"Ada", 37}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("args = %#v, want %#v", args, wantArgs)
	}
}

func TestWriterRejectsInvalidIdentifier(t *testing.T) {
	writer := NewWriter(fakeExecer{}, SQLite, WriterConfig{})

	_, err := writer.Insert(context.Background(), "users;drop", testUser{Name: "Ada"})
	if !errors.Is(err, ErrInvalidIdentifier) {
		t.Fatalf("err = %v, want ErrInvalidIdentifier", err)
	}
}

func TestOpenWithGORMDialector(t *testing.T) {
	database, err := Open(context.Background(), Config{
		GORMDialector: sqlite.Open(":memory:"),
		Dialect:       SQLite,
		MaxOpenConns:  1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestDatabase(t, database)
	if database.GORM() == nil {
		t.Fatal("GORM handle is nil")
	}
	store := NewStore[testUser](database, "users")
	if store.GORM() == nil {
		t.Fatal("typed GORM store handle is nil")
	}
	createTestUsersTable(t, database.DB())
	if _, err := store.Create(context.Background(), &testUser{Name: "GORM", Age: 9}); err != nil {
		t.Fatalf("store create: %v", err)
	}
	assertUserCount(t, database.DB(), 1)
}

func TestGenericStoreCreateAndAsync(t *testing.T) {
	database := openTestSQLite(t)
	defer closeTestDatabase(t, database)
	createTestUsersTable(t, database.DB())

	store := NewStore[testUser](database, "users")
	if _, err := store.Create(context.Background(), &testUser{Name: "Store", Age: 11}); err != nil {
		t.Fatalf("store create: %v", err)
	}
	future, err := store.Async().Create(context.Background(), &testUser{Name: "Async Store", Age: 12})
	if err != nil {
		t.Fatalf("async store create: %v", err)
	}
	if _, err := future.Wait(context.Background()); err != nil {
		t.Fatalf("async wait: %v", err)
	}
	assertUserCount(t, database.DB(), 2)
}
func TestWriterSQLiteIntegration(t *testing.T) {
	ctx := context.Background()
	database := openTestSQLite(t)
	defer closeTestDatabase(t, database)
	createTestUsersTable(t, database.DB())

	if _, err := database.Writer().Insert(ctx, "users", testUser{Name: "Ada", Age: 37}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	assertUserCount(t, database.DB(), 1)

	if _, err := database.Writer().Update(ctx, "users", testUser{ID: 1, Name: "Ada Lovelace", Age: 38}); err != nil {
		t.Fatalf("update: %v", err)
	}
	assertUserName(t, database.DB(), 1, "Ada Lovelace")

	if _, err := database.Writer().Upsert(ctx, "users", testUpsertUser{ID: 1, Name: "Ada", Age: 39}, Conflict("id")); err != nil {
		t.Fatalf("upsert update: %v", err)
	}
	assertUserName(t, database.DB(), 1, "Ada")

	users := []testUser{{Name: "Grace", Age: 30}, {Name: "Linus", Age: 40}}
	if _, err := database.Writer().BatchInsert(ctx, "users", users); err != nil {
		t.Fatalf("batch insert: %v", err)
	}
	assertUserCount(t, database.DB(), 3)
}

func TestDatabaseTxCommitAndRollback(t *testing.T) {
	ctx := context.Background()
	database := openTestSQLite(t)
	defer closeTestDatabase(t, database)
	createTestUsersTable(t, database.DB())

	if err := database.Tx(ctx, func(tx *Tx) error {
		_, err := tx.Writer().Insert(ctx, "users", testUser{Name: "Commit", Age: 1})
		return err
	}); err != nil {
		t.Fatalf("commit tx: %v", err)
	}
	assertUserCount(t, database.DB(), 1)

	errRollback := errors.New("rollback")
	err := database.Tx(ctx, func(tx *Tx) error {
		if _, err := tx.Writer().Insert(ctx, "users", testUser{Name: "Rollback", Age: 2}); err != nil {
			return err
		}
		return errRollback
	})
	if !errors.Is(err, errRollback) {
		t.Fatalf("rollback err = %v, want %v", err, errRollback)
	}
	assertUserCount(t, database.DB(), 1)
}

func TestAsyncWriterQueueFullAndShutdown(t *testing.T) {
	block := make(chan struct{})
	started := make(chan struct{})
	var closeBlock sync.Once
	async := NewAsyncWriter(nil, AsyncConfig{Workers: 1, QueueSize: 1})
	t.Cleanup(func() {
		closeBlock.Do(func() { close(block) })
		_ = async.Shutdown(context.Background())
	})

	first, err := async.Submit(context.Background(), func(context.Context) (sql.Result, error) {
		close(started)
		<-block
		return nil, nil
	})
	if err != nil {
		t.Fatalf("submit first: %v", err)
	}
	<-started
	second, err := async.Submit(context.Background(), func(context.Context) (sql.Result, error) {
		return nil, nil
	})
	if err != nil {
		t.Fatalf("submit second: %v", err)
	}
	if _, err := async.Submit(context.Background(), func(context.Context) (sql.Result, error) {
		return nil, nil
	}); !errors.Is(err, ErrAsyncQueueFull) {
		t.Fatalf("submit third err = %v, want ErrAsyncQueueFull", err)
	}

	closeBlock.Do(func() { close(block) })
	if _, err := first.Wait(context.Background()); err != nil {
		t.Fatalf("first wait: %v", err)
	}
	if _, err := second.Wait(context.Background()); err != nil {
		t.Fatalf("second wait: %v", err)
	}
	if err := async.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if _, err := async.Submit(context.Background(), func(context.Context) (sql.Result, error) {
		return nil, nil
	}); !errors.Is(err, ErrAsyncClosed) {
		t.Fatalf("submit after shutdown err = %v, want ErrAsyncClosed", err)
	}
}

func TestAsyncWriterSQLiteInsert(t *testing.T) {
	ctx := context.Background()
	database := openTestSQLite(t)
	defer closeTestDatabase(t, database)
	createTestUsersTable(t, database.DB())

	future, err := database.Async().Insert(ctx, "users", testUser{Name: "Async", Age: 7})
	if err != nil {
		t.Fatalf("async insert: %v", err)
	}
	if _, err := future.Wait(ctx); err != nil {
		t.Fatalf("future wait: %v", err)
	}
	assertUserCount(t, database.DB(), 1)
}

func openTestSQLite(t *testing.T) *Database {
	t.Helper()
	database, err := Open(context.Background(), Config{
		DriverName:   "sqlite",
		DSN:          ":memory:",
		Dialect:      SQLite,
		MaxOpenConns: 1,
		Async: AsyncConfig{
			Workers:   1,
			QueueSize: 4,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return database
}

func closeTestDatabase(t *testing.T, database *Database) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := database.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

func createTestUsersTable(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), `
CREATE TABLE users (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL,
	age INTEGER NOT NULL
)`)
	if err != nil {
		t.Fatal(err)
	}
}

func assertUserCount(t *testing.T, db *sql.DB, want int) {
	t.Helper()
	var count int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM users`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("count = %d, want %d", count, want)
	}
}

func assertUserName(t *testing.T, db *sql.DB, id int64, want string) {
	t.Helper()
	var name string
	if err := db.QueryRowContext(context.Background(), `SELECT name FROM users WHERE id = ?`, id).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != want {
		t.Fatalf("name = %q, want %q", name, want)
	}
}

type fakeExecer struct{}

func (fakeExecer) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	return nil, nil
}
