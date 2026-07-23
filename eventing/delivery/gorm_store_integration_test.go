//go:build dbintegration

package delivery

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	gormstore "github.com/saramand9/bignet/kpro/database/gorm"
	"github.com/saramand9/bignet/kpro/eventing/events"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestExternalDatabaseDeliveryLeaseClock(t *testing.T) {
	tests := []struct {
		name      string
		dsnEnv    string
		dialector func(string) gorm.Dialector
	}{
		{name: "mysql", dsnEnv: "KPRO_TEST_MYSQL_DSN", dialector: mysql.Open},
		{name: "postgres", dsnEnv: "KPRO_TEST_POSTGRES_DSN", dialector: postgres.Open},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dsn := os.Getenv(test.dsnEnv)
			if dsn == "" {
				if os.Getenv("KPRO_REQUIRE_EXTERNAL_DB") == "1" {
					t.Fatalf("required environment variable %s is empty", test.dsnEnv)
				}
				t.Skipf("%s is not configured", test.dsnEnv)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			database, err := gormstore.Open(ctx, gormstore.Config{
				Name:         "delivery-integration-" + test.name,
				Dialector:    test.dialector(dsn),
				MaxOpenConns: 4,
				MaxIdleConns: 4,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := database.Close(); err != nil {
					t.Errorf("close database: %v", err)
				}
			}()
			if err := InitGORM(ctx, database); err != nil {
				t.Fatal(err)
			}

			token := fmt.Sprintf("%s-%d", test.name, time.Now().UnixNano())
			store := NewGORMStore(database).WithLease(5 * time.Second)
			envelope := events.Envelope{
				ID:            "db-clock-" + token,
				Type:          "integration.clock",
				AggregateID:   token,
				SchemaVersion: 1,
				OccurredAt:    time.Now().UTC(),
				Payload:       []byte(`{"ok":true}`),
			}
			consumer := "db-clock-" + token
			if err := store.Append(ctx, envelope); err != nil {
				t.Fatal(err)
			}
			records, err := store.Fetch(ctx, consumer, 1)
			if err != nil {
				t.Fatal(err)
			}
			if len(records) != 1 || records[0].Attempts != 1 {
				t.Fatalf("records=%#v, want one first attempt", records)
			}
			if _, err := store.RenewAttempt(ctx, consumer, envelope.ID, records[0].Attempts); err != nil {
				t.Fatal(err)
			}
			if err := store.AckAttempt(ctx, consumer, envelope.ID, records[0].Attempts); err != nil {
				t.Fatal(err)
			}
			if records, err := store.Fetch(ctx, consumer, 1); err != nil || len(records) != 0 {
				t.Fatalf("fetch after ack records=%#v err=%v", records, err)
			}

			store.WithRetry(1, time.Millisecond)
			batch := make([]events.Envelope, 3)
			for i := range batch {
				batch[i] = events.Envelope{
					ID:            fmt.Sprintf("db-batch-%s-%d", token, i),
					Type:          "integration.batch",
					AggregateID:   token,
					SchemaVersion: 1,
					OccurredAt:    time.Now().UTC().Add(time.Duration(i) * time.Microsecond),
					Payload:       []byte(`{"batch":true}`),
				}
			}
			if err := store.Append(ctx, batch...); err != nil {
				t.Fatal(err)
			}
			stats, err := store.Backlog(ctx, consumer)
			if err != nil {
				t.Fatal(err)
			}
			if stats.Pending != int64(len(batch)) {
				t.Fatalf("pending backlog=%d, want %d; stats=%#v", stats.Pending, len(batch), stats)
			}
			batchRecords, err := store.Fetch(ctx, consumer, len(batch))
			if err != nil {
				t.Fatal(err)
			}
			if len(batchRecords) != len(batch) {
				t.Fatalf("batch records=%d, want %d", len(batchRecords), len(batch))
			}
			attempts := make([]LeaseAttempt, len(batchRecords))
			for i, record := range batchRecords {
				attempts[i] = LeaseAttempt{Consumer: consumer, EventID: record.Envelope.ID, Attempt: record.Attempts}
			}
			renewals, err := store.RenewAttempts(ctx, attempts)
			if err != nil {
				t.Fatal(err)
			}
			for _, renewal := range renewals {
				if renewal.Lost || renewal.Remaining <= 0 {
					t.Fatalf("batch renewal=%#v", renewal)
				}
				if err := store.FailAttempt(ctx, renewal.Consumer, renewal.EventID, renewal.Attempt, errors.New("integration dead letter")); err != nil {
					t.Fatal(err)
				}
			}
			letters, err := store.DeadLetters(ctx, consumer, 10)
			if err != nil {
				t.Fatal(err)
			}
			if len(letters) != len(batch) {
				t.Fatalf("dead letters=%d, want %d", len(letters), len(batch))
			}
			if err := store.ReplayDeadLetter(ctx, consumer, letters[0].Record.Envelope.ID); err != nil {
				t.Fatal(err)
			}
			if err := store.ReplayDeadLetter(ctx, consumer, letters[0].Record.Envelope.ID); !errors.Is(err, ErrNotDead) {
				t.Fatalf("second replay error=%v, want ErrNotDead", err)
			}
			stats, err = store.Backlog(ctx, consumer)
			if err != nil {
				t.Fatal(err)
			}
			if stats.Pending != 1 || stats.Dead != int64(len(batch)-1) {
				t.Fatalf("backlog after replay=%#v", stats)
			}
		})
	}
}
