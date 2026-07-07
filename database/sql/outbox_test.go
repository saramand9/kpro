package sqlstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/saramand9/bignet/kpro/database/dbx"
	"github.com/saramand9/bignet/kpro/database/facts"
	"github.com/saramand9/bignet/kpro/database/outbox"

	_ "modernc.org/sqlite"
)

type outboxTestFact struct {
	ID string `json:"id"`
}

func (f outboxTestFact) FactType() string    { return "test.fact" }
func (f outboxTestFact) AggregateID() string { return f.ID }

func TestOutboxStoreAppendFetchAckPerConsumer(t *testing.T) {
	ctx := context.Background()
	database := openOutboxTestDatabase(t)
	store := NewOutboxStore(database, OutboxConfig{})
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}

	envelope := testEnvelope(t, "f1", outboxTestFact{ID: "u1"})
	if err := store.Append(ctx, envelope); err != nil {
		t.Fatal(err)
	}

	cacheRecords, err := store.Fetch(ctx, "cache", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(cacheRecords) != 1 {
		t.Fatalf("expected one cache record, got %d", len(cacheRecords))
	}
	if cacheRecords[0].Envelope.ID != "f1" {
		t.Fatalf("expected fact f1, got %q", cacheRecords[0].Envelope.ID)
	}

	if err := store.Ack(ctx, "cache", "f1"); err != nil {
		t.Fatal(err)
	}
	if err := store.Ack(ctx, "cache", "f1"); err != nil {
		t.Fatalf("ack should be idempotent: %v", err)
	}
	cacheRecords, err = store.Fetch(ctx, "cache", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(cacheRecords) != 0 {
		t.Fatalf("expected cache consumer to be caught up, got %d records", len(cacheRecords))
	}

	searchRecords, err := store.Fetch(ctx, "search", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(searchRecords) != 1 {
		t.Fatalf("expected independent search delivery, got %d records", len(searchRecords))
	}
}

func TestOutboxStoreFailRetriesThenDeadLetters(t *testing.T) {
	ctx := context.Background()
	database := openOutboxTestDatabase(t)
	store := NewOutboxStore(database, OutboxConfig{
		RetryDelay:  0,
		MaxAttempts: 2,
	})
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(ctx, testEnvelope(t, "f1", outboxTestFact{ID: "u1"})); err != nil {
		t.Fatal(err)
	}

	records, err := store.Fetch(ctx, "cache", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("expected first delivery, got %d records", len(records))
	}
	if err := store.Fail(ctx, "cache", "f1", errors.New("temporary")); err != nil {
		t.Fatal(err)
	}

	records, err = store.Fetch(ctx, "cache", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("expected retry delivery, got %d records", len(records))
	}
	if records[0].Attempts != 1 || records[0].Status != outbox.StatusInflight {
		t.Fatalf("expected claimed retry attempt 1, got status=%s attempts=%d", records[0].Status, records[0].Attempts)
	}
	if err := store.Fail(ctx, "cache", "f1", errors.New("permanent")); err != nil {
		t.Fatal(err)
	}

	records, err = store.Fetch(ctx, "cache", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("expected dead-lettered fact to stop fetching, got %d records", len(records))
	}
}

func TestOutboxStoreFetchClaimsRecords(t *testing.T) {
	ctx := context.Background()
	database := openOutboxTestDatabase(t)
	store := NewOutboxStore(database, OutboxConfig{})
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(ctx, testEnvelope(t, "f1", outboxTestFact{ID: "u1"})); err != nil {
		t.Fatal(err)
	}

	records, err := store.Fetch(ctx, "cache", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("expected claimed record, got %d", len(records))
	}
	if records[0].Status != outbox.StatusInflight {
		t.Fatalf("expected inflight record, got %s", records[0].Status)
	}

	records, err = store.Fetch(ctx, "cache", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("expected inflight record not to be refetched, got %d", len(records))
	}
}

func TestOutboxStoreReclaimsExpiredInflightRecords(t *testing.T) {
	ctx := context.Background()
	database := openOutboxTestDatabase(t)
	store := NewOutboxStore(database, OutboxConfig{
		ClaimTimeout: time.Nanosecond,
	})
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(ctx, testEnvelope(t, "f1", outboxTestFact{ID: "u1"})); err != nil {
		t.Fatal(err)
	}
	records, err := store.Fetch(ctx, "cache", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("expected first claim, got %d", len(records))
	}
	time.Sleep(time.Millisecond)
	records, err = store.Fetch(ctx, "cache", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Envelope.ID != "f1" {
		t.Fatalf("expected expired inflight record to be reclaimed, got %#v", records)
	}
}

func openOutboxTestDatabase(t *testing.T) *dbx.Database {
	t.Helper()
	database, err := dbx.Open(context.Background(), dbx.Config{
		DriverName:   "sqlite",
		DSN:          "file:outbox_test?mode=memory&cache=shared",
		Dialect:      dbx.SQLite,
		MaxOpenConns: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatal(err)
		}
	})
	return database
}

func testEnvelope(t *testing.T, id string, fact outboxTestFact) facts.Envelope {
	t.Helper()
	payload, err := (facts.JSONCodec{}).Encode(fact)
	if err != nil {
		t.Fatal(err)
	}
	return facts.NewEnvelope(id, fact, payload, time.Unix(100, 0).UTC())
}
