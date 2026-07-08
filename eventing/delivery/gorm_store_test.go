package delivery

import (
	"context"
	"errors"
	"testing"
	"time"

	gormstore "github.com/saramand9/bignet/kpro/database/gorm"
	"github.com/saramand9/bignet/kpro/eventing/events"
	"gorm.io/driver/sqlite"

	_ "modernc.org/sqlite"
)

func TestGORMStoreAppendFetchAck(t *testing.T) {
	database := openDeliveryTestDatabase(t)
	defer closeDeliveryTestDatabase(t, database)
	store := NewGORMStore(database).WithRetry(2, 0)

	envelope := testEnvelope("evt-1")
	if err := store.Append(context.Background(), envelope); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(context.Background(), envelope); err != nil {
		t.Fatal(err)
	}

	records, err := store.Fetch(context.Background(), "search-projector", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("records=%d, want 1", len(records))
	}
	if records[0].Envelope.ID != envelope.ID || records[0].Status != StatusInflight || records[0].Attempts != 1 {
		t.Fatalf("record=%#v", records[0])
	}

	if err := store.Ack(context.Background(), "search-projector", envelope.ID); err != nil {
		t.Fatal(err)
	}
	records, err = store.Fetch(context.Background(), "search-projector", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("records after ack=%#v", records)
	}
}

func TestGORMStoreFetchSkipsAlreadyInflightRecords(t *testing.T) {
	database := openDeliveryTestDatabase(t)
	defer closeDeliveryTestDatabase(t, database)
	store := NewGORMStore(database)

	envelope := testEnvelope("evt-3")
	if err := store.Append(context.Background(), envelope); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Fetch(context.Background(), "cache-projector", 1); err != nil {
		t.Fatal(err)
	}

	records, err := store.Fetch(context.Background(), "cache-projector", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("inflight records should not be fetched again: %#v", records)
	}
}
func TestGORMStoreFailRetriesAndDeadLetters(t *testing.T) {
	database := openDeliveryTestDatabase(t)
	defer closeDeliveryTestDatabase(t, database)
	store := NewGORMStore(database).WithRetry(2, 0)

	envelope := testEnvelope("evt-2")
	if err := store.Append(context.Background(), envelope); err != nil {
		t.Fatal(err)
	}

	records, err := store.Fetch(context.Background(), "graph-projector", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Attempts != 1 {
		t.Fatalf("first fetch=%#v", records)
	}
	if err := store.Fail(context.Background(), "graph-projector", envelope.ID, errors.New("boom")); err != nil {
		t.Fatal(err)
	}

	records, err = store.Fetch(context.Background(), "graph-projector", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Attempts != 2 || records[0].LastError != "boom" {
		t.Fatalf("retry fetch=%#v", records)
	}
	if err := store.Fail(context.Background(), "graph-projector", envelope.ID, errors.New("again")); err != nil {
		t.Fatal(err)
	}

	records, err = store.Fetch(context.Background(), "graph-projector", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("dead records should not be fetched: %#v", records)
	}
}

func openDeliveryTestDatabase(t *testing.T) *gormstore.Database {
	t.Helper()
	database, err := gormstore.Open(context.Background(), gormstore.Config{
		Dialector:    sqlite.Dialector{DriverName: "sqlite", DSN: ":memory:"},
		MaxOpenConns: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := InitGORM(context.Background(), database); err != nil {
		t.Fatal(err)
	}
	return database
}

func closeDeliveryTestDatabase(t *testing.T, database *gormstore.Database) {
	t.Helper()
	if err := database.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func testEnvelope(id string) events.Envelope {
	return events.Envelope{
		ID:            id,
		Type:          "user.created",
		AggregateID:   "user-1",
		TenantID:      "tenant-1",
		ActorID:       "actor-1",
		TraceID:       "trace-1",
		SchemaVersion: 1,
		OccurredAt:    time.Now().UTC(),
		Payload:       []byte(`{"name":"Ada"}`),
	}
}
