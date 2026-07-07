//go:build dbintegration

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/saramand9/bignet/kpro/database/dbx"
	"github.com/saramand9/bignet/kpro/database/facts"
	"github.com/saramand9/bignet/kpro/database/outbox"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

type integrationBackend struct {
	name       string
	driverName string
	dsn        string
	dialect    dbx.Dialect
}

type integrationRecord struct {
	ID        int64  `db:"id,pk"`
	Name      string `db:"name"`
	Age       int    `db:"age"`
	CreatedAt string `db:"created_at,omitempty"`
}

type integrationFact struct {
	ID string `json:"id"`
}

var integrationNameCounter atomic.Uint64

func (f integrationFact) FactType() string    { return "integration.fact" }
func (f integrationFact) AggregateID() string { return f.ID }

func TestSQLBackendsStoreMigratorAndOutbox(t *testing.T) {
	for _, backend := range integrationBackends(t) {
		backend := backend
		t.Run(backend.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			database := openIntegrationDatabase(t, backend)
			defer database.Close()

			testIntegrationStoreAndMigrator(t, ctx, database, backend)
			testIntegrationOutbox(t, ctx, database, backend)
		})
	}
}

func integrationBackends(t *testing.T) []integrationBackend {
	t.Helper()
	backends := []integrationBackend{
		{
			name:       "sqlite",
			driverName: "sqlite",
			dsn:        fmt.Sprintf("file:%s?mode=memory&cache=shared", uniqueSQLName("dbit_sqlite")),
			dialect:    dbx.SQLite,
		},
	}
	if dsn := os.Getenv("KPRO_TEST_MYSQL_DSN"); dsn != "" {
		backends = append(backends, integrationBackend{
			name:       "mysql",
			driverName: "mysql",
			dsn:        dsn,
			dialect:    dbx.MySQL,
		})
	}
	if dsn := os.Getenv("KPRO_TEST_POSTGRES_DSN"); dsn != "" {
		backends = append(backends, integrationBackend{
			name:       "postgres",
			driverName: "pgx",
			dsn:        dsn,
			dialect:    dbx.Postgres,
		})
	}
	if os.Getenv("KPRO_REQUIRE_EXTERNAL_DB") == "1" && len(backends) == 1 {
		t.Fatal("KPRO_REQUIRE_EXTERNAL_DB=1 but no MySQL or Postgres DSN was configured")
	}
	return backends
}

func openIntegrationDatabase(t *testing.T, backend integrationBackend) *dbx.Database {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	database, err := dbx.Open(ctx, dbx.Config{
		DriverName:   backend.driverName,
		DSN:          backend.dsn,
		Dialect:      backend.dialect,
		MaxOpenConns: 4,
		MaxIdleConns: 2,
		Async: dbx.AsyncConfig{
			Workers:   2,
			QueueSize: 16,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return database
}

func testIntegrationStoreAndMigrator(t *testing.T, ctx context.Context, database *dbx.Database, backend integrationBackend) {
	t.Helper()
	table := uniqueSQLName("dbit_records")
	defer dropIntegrationTable(t, ctx, database, table)

	migrator := NewMigrator(database, "")
	migration := Migration{
		ID:          table + "_001",
		Description: "create integration record table",
		Statements:  []string{integrationRecordSchema(backend.dialect, table)},
	}
	if err := migrator.Apply(ctx, migration); err != nil {
		t.Fatal(err)
	}
	if err := migrator.Apply(ctx, migration); err != nil {
		t.Fatal(err)
	}

	store := dbx.NewStore[integrationRecord](database, table)
	if _, err := store.Create(ctx, &integrationRecord{ID: 1, Name: "Ada", Age: 37}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := store.Update(ctx, &integrationRecord{ID: 1, Name: "Ada Lovelace", Age: 38}); err != nil {
		t.Fatalf("update: %v", err)
	}
	record, err := store.Find(ctx, 1)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if record.Name != "Ada Lovelace" || record.Age != 38 {
		t.Fatalf("record = %#v", record)
	}

	errRollback := errors.New("rollback")
	err = database.Tx(ctx, func(tx *dbx.Tx) error {
		txCtx := dbx.WithTx(ctx, tx)
		if _, err := store.Create(txCtx, &integrationRecord{ID: 2, Name: "Rollback", Age: 1}); err != nil {
			return err
		}
		if _, err := store.Find(txCtx, 2); err != nil {
			return err
		}
		return errRollback
	})
	if !errors.Is(err, errRollback) {
		t.Fatalf("tx err = %v, want rollback", err)
	}
	if _, err := store.Find(ctx, 2); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("find rolled back row err = %v, want sql.ErrNoRows", err)
	}

	if _, err := store.Upsert(ctx, &integrationRecord{ID: 1, Name: "Ada", Age: 39}, dbx.Conflict("id")); err != nil {
		t.Fatalf("upsert update: %v", err)
	}
	if _, err := store.BatchCreate(ctx, []integrationRecord{
		{ID: 3, Name: "Grace", Age: 30},
		{ID: 4, Name: "Linus", Age: 40},
	}); err != nil {
		t.Fatalf("batch create: %v", err)
	}
	records, err := store.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("records len = %d, want 3", len(records))
	}
}

func testIntegrationOutbox(t *testing.T, ctx context.Context, database *dbx.Database, backend integrationBackend) {
	t.Helper()
	factTable := uniqueSQLName("dbit_facts")
	deliveryTable := uniqueSQLName("dbit_deliveries")
	defer dropIntegrationTable(t, ctx, database, deliveryTable)
	defer dropIntegrationTable(t, ctx, database, factTable)

	store := NewOutboxStore(database, OutboxConfig{
		FactTable:     factTable,
		DeliveryTable: deliveryTable,
		RetryDelay:    0,
		ClaimTimeout:  time.Minute,
		MaxAttempts:   2,
	})
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	envelopes := make([]facts.Envelope, 0, 20)
	for i := 0; i < 20; i++ {
		envelopes = append(envelopes, integrationEnvelope(t, fmt.Sprintf("f%02d", i)))
	}
	if err := store.Append(ctx, envelopes...); err != nil {
		t.Fatal(err)
	}

	results := make(chan []outbox.Record, 2)
	if backend.dialect == dbx.SQLite {
		records, err := store.Fetch(ctx, "cache", 20)
		if err != nil {
			t.Fatal(err)
		}
		results <- records
		close(results)
	} else {
		var wg sync.WaitGroup
		errs := make(chan error, 2)
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				records, err := store.Fetch(ctx, "cache", 10)
				if err != nil {
					errs <- err
					return
				}
				results <- records
			}()
		}
		wg.Wait()
		close(results)
		close(errs)
		for err := range errs {
			t.Fatal(err)
		}
	}
	claimed := make(map[string]struct{})
	for records := range results {
		for _, record := range records {
			if record.Status != outbox.StatusInflight {
				t.Fatalf("claimed status = %s, want inflight", record.Status)
			}
			if _, exists := claimed[record.Envelope.ID]; exists {
				t.Fatalf("duplicate claim for %s", record.Envelope.ID)
			}
			claimed[record.Envelope.ID] = struct{}{}
		}
	}
	if len(claimed) != 20 {
		t.Fatalf("claimed = %d, want 20", len(claimed))
	}

	if err := store.Fail(ctx, "cache", "f00", errors.New("temporary")); err != nil {
		t.Fatal(err)
	}
	records, err := store.Fetch(ctx, "cache", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Envelope.ID != "f00" || records[0].Attempts != 1 {
		t.Fatalf("retry records = %#v", records)
	}
	if err := store.Fail(ctx, "cache", "f00", errors.New("permanent")); err != nil {
		t.Fatal(err)
	}
	records, err = store.Fetch(ctx, "cache", 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if record.Envelope.ID == "f00" {
			t.Fatal("dead-lettered fact was fetched again")
		}
	}
}

func integrationRecordSchema(dialect dbx.Dialect, table string) string {
	switch dialect {
	case dbx.MySQL:
		return fmt.Sprintf(`CREATE TABLE %s (
id BIGINT NOT NULL PRIMARY KEY,
name VARCHAR(255) NOT NULL,
age INTEGER NOT NULL,
created_at VARCHAR(64) NOT NULL DEFAULT ''
)`, table)
	default:
		return fmt.Sprintf(`CREATE TABLE %s (
id BIGINT PRIMARY KEY,
name VARCHAR(255) NOT NULL,
age INTEGER NOT NULL,
created_at VARCHAR(64) NOT NULL DEFAULT ''
)`, table)
	}
}

func integrationEnvelope(t *testing.T, id string) facts.Envelope {
	t.Helper()
	fact := integrationFact{ID: id}
	payload, err := (facts.JSONCodec{}).Encode(fact)
	if err != nil {
		t.Fatal(err)
	}
	return facts.NewEnvelope(id, fact, payload, time.Now().UTC())
}

func uniqueSQLName(prefix string) string {
	return fmt.Sprintf("%s_%d_%d", prefix, time.Now().UnixNano(), integrationNameCounter.Add(1))
}

func dropIntegrationTable(t *testing.T, ctx context.Context, database *dbx.Database, table string) {
	t.Helper()
	if _, err := database.DB().ExecContext(ctx, "DROP TABLE IF EXISTS "+table); err != nil {
		t.Logf("drop table %s: %v", table, err)
	}
}
