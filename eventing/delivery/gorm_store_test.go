package delivery

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	gormstore "github.com/saramand9/bignet/kpro/database/gorm"
	"github.com/saramand9/bignet/kpro/eventing/events"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

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

func TestGORMStoreUsesDatabaseLeaseClockByDefault(t *testing.T) {
	database := openDeliveryTestDatabase(t)
	defer closeDeliveryTestDatabase(t, database)
	store := NewGORMStore(database)
	if store.now != nil {
		t.Fatal("production store unexpectedly uses an application clock")
	}

	before := time.Now().UTC().Add(-100 * time.Millisecond)
	got, err := store.leaseNow(context.Background(), database.GORM())
	if err != nil {
		t.Fatal(err)
	}
	after := time.Now().UTC().Add(100 * time.Millisecond)
	if got.Before(before) || got.After(after) {
		t.Fatalf("database lease clock = %s, want between %s and %s", got, before, after)
	}
}

func TestGORMStoreRenewAttemptReturnsRemainingLeaseDuration(t *testing.T) {
	database := openDeliveryTestDatabase(t)
	defer closeDeliveryTestDatabase(t, database)
	const lease = 100 * time.Millisecond
	store := NewGORMStore(database).WithLease(lease)
	ctx := context.Background()
	envelope := testEnvelope("evt-renew-remaining")
	if err := store.Append(ctx, envelope); err != nil {
		t.Fatal(err)
	}
	records, err := store.Fetch(ctx, "renew-remaining", 1)
	if err != nil || len(records) != 1 {
		t.Fatalf("fetch records=%#v err=%v", records, err)
	}
	if err := database.GORM().Callback().Update().Before("gorm:update").Register(
		"test:slow-renewal",
		func(*gorm.DB) { time.Sleep(20 * time.Millisecond) },
	); err != nil {
		t.Fatal(err)
	}

	remaining, err := store.RenewAttempt(ctx, "renew-remaining", envelope.ID, records[0].Attempts)
	if err != nil {
		t.Fatal(err)
	}
	if remaining <= 0 || remaining >= 90*time.Millisecond {
		t.Fatalf("remaining lease = %s, want positive duration below 90ms", remaining)
	}
}

func TestGORMStoreClampsLeaseToDatabasePrecision(t *testing.T) {
	store := NewGORMStore(nil).WithLease(time.Nanosecond)
	if store.leaseDuration != minimumLeaseDuration {
		t.Fatalf("lease duration = %s, want %s", store.leaseDuration, minimumLeaseDuration)
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

func TestGORMStoreReclaimsRecordAfterLeaseExpires(t *testing.T) {
	database := openDeliveryTestDatabase(t)
	defer closeDeliveryTestDatabase(t, database)
	now := time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC)
	store := NewGORMStore(database).WithLease(time.Minute)
	store.now = func() time.Time { return now }

	envelope := testEnvelope("evt-lease")
	if err := store.Append(context.Background(), envelope); err != nil {
		t.Fatal(err)
	}
	first, err := store.Fetch(context.Background(), "lease-projector", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || first[0].Attempts != 1 {
		t.Fatalf("first fetch=%#v", first)
	}
	if want := now.Add(time.Minute); !first[0].AvailableAt.Equal(want) {
		t.Fatalf("lease expiry=%s, want %s", first[0].AvailableAt, want)
	}

	now = now.Add(time.Minute - time.Nanosecond)
	locked, err := store.Fetch(context.Background(), "lease-projector", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(locked) != 0 {
		t.Fatalf("unexpired lease fetched again: %#v", locked)
	}

	now = now.Add(time.Nanosecond)
	reclaimed, err := store.Fetch(context.Background(), "lease-projector", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(reclaimed) != 1 || reclaimed[0].Attempts != 2 {
		t.Fatalf("reclaimed fetch=%#v", reclaimed)
	}
	if err := store.AckAttempt(context.Background(), "lease-projector", envelope.ID, reclaimed[0].Attempts); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	afterAck, err := store.Fetch(context.Background(), "lease-projector", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(afterAck) != 0 {
		t.Fatalf("acked record fetched again: %#v", afterAck)
	}
}

func TestGORMStoreExpiredLeaseHonorsMaxAttempts(t *testing.T) {
	database := openDeliveryTestDatabase(t)
	defer closeDeliveryTestDatabase(t, database)
	now := time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC)
	store := NewGORMStore(database).WithRetry(1, 0).WithLease(time.Minute)
	store.now = func() time.Time { return now }

	envelope := testEnvelope("evt-dead-lease")
	if err := store.Append(context.Background(), envelope); err != nil {
		t.Fatal(err)
	}
	if records, err := store.Fetch(context.Background(), "dead-projector", 1); err != nil || len(records) != 1 {
		t.Fatalf("first fetch records=%#v err=%v", records, err)
	}
	now = now.Add(time.Minute)
	if records, err := store.Fetch(context.Background(), "dead-projector", 1); err != nil || len(records) != 0 {
		t.Fatalf("expired max-attempt record records=%#v err=%v", records, err)
	}

	var row outboxDeliveryRow
	if err := database.GORM().First(&row, "event_id = ? AND consumer = ?", envelope.ID, "dead-projector").Error; err != nil {
		t.Fatal(err)
	}
	if Status(row.Status) != StatusDead {
		t.Fatalf("status=%s, want %s", row.Status, StatusDead)
	}
}

func TestGORMStoreRenewAttemptExtendsOnlyCurrentLiveLease(t *testing.T) {
	database := openDeliveryTestDatabase(t)
	defer closeDeliveryTestDatabase(t, database)
	now := time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC)
	store := NewGORMStore(database).WithLease(time.Minute)
	store.now = func() time.Time { return now }
	ctx := context.Background()
	consumer := "renew-projector"
	envelope := testEnvelope("evt-renew")
	if err := store.Append(ctx, envelope); err != nil {
		t.Fatal(err)
	}
	records, err := store.Fetch(ctx, consumer, 1)
	if err != nil || len(records) != 1 {
		t.Fatalf("fetch=%#v err=%v", records, err)
	}

	now = now.Add(30 * time.Second)
	renewedFor, err := store.RenewAttempt(ctx, consumer, envelope.ID, records[0].Attempts)
	if err != nil {
		t.Fatal(err)
	}
	if renewedFor <= 0 || renewedFor > time.Minute {
		t.Fatalf("renewed lease duration=%s, want within (0, %s]", renewedFor, time.Minute)
	}
	now = now.Add(time.Minute)
	if _, err := store.RenewAttempt(ctx, consumer, envelope.ID, records[0].Attempts); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("expired renewal error=%v, want ErrLeaseLost", err)
	}
}

func TestGORMStoreAppendTxCommitsAndRollsBackAtomically(t *testing.T) {
	database := openDeliveryTestDatabase(t)
	defer closeDeliveryTestDatabase(t, database)
	store := NewGORMStore(database)
	ctx := context.Background()
	if err := database.GORM().Exec(`CREATE TABLE delivery_tx_entities (id TEXT PRIMARY KEY)`).Error; err != nil {
		t.Fatal(err)
	}

	rollbackCause := errors.New("rollback")
	err := database.GORM().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`INSERT INTO delivery_tx_entities (id) VALUES (?)`, "rolled-back").Error; err != nil {
			return err
		}
		if err := store.AppendTx(ctx, tx, testEnvelope("evt-rolled-back")); err != nil {
			return err
		}
		return rollbackCause
	})
	if !errors.Is(err, rollbackCause) {
		t.Fatalf("transaction error=%v, want %v", err, rollbackCause)
	}

	if err := database.GORM().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`INSERT INTO delivery_tx_entities (id) VALUES (?)`, "committed").Error; err != nil {
			return err
		}
		return store.AppendTx(ctx, tx, testEnvelope("evt-committed"))
	}); err != nil {
		t.Fatal(err)
	}

	var entityIDs []string
	if err := database.GORM().Table("delivery_tx_entities").Order("id").Pluck("id", &entityIDs).Error; err != nil {
		t.Fatal(err)
	}
	if len(entityIDs) != 1 || entityIDs[0] != "committed" {
		t.Fatalf("entities=%#v", entityIDs)
	}
	records, err := store.Fetch(ctx, "tx-projector", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Envelope.ID != "evt-committed" {
		t.Fatalf("outbox records=%#v", records)
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
	if err := store.FailAttempt(context.Background(), "graph-projector", envelope.ID, records[0].Attempts, errors.New("boom")); err != nil {
		t.Fatal(err)
	}

	records, err = store.Fetch(context.Background(), "graph-projector", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Attempts != 2 || records[0].LastError != "boom" {
		t.Fatalf("retry fetch=%#v", records)
	}
	if err := store.FailAttempt(context.Background(), "graph-projector", envelope.ID, records[0].Attempts, errors.New("again")); err != nil {
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

func TestGORMStoreRejectsStaleAttemptSettlement(t *testing.T) {
	database := openDeliveryTestDatabase(t)
	defer closeDeliveryTestDatabase(t, database)
	now := time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC)
	store := NewGORMStore(database).WithLease(time.Minute)
	store.now = func() time.Time { return now }
	ctx := context.Background()
	consumer := "fenced-projector"
	envelope := testEnvelope("evt-fenced")
	if err := store.Append(ctx, envelope); err != nil {
		t.Fatal(err)
	}

	first, err := store.Fetch(ctx, consumer, 1)
	if err != nil || len(first) != 1 {
		t.Fatalf("first fetch=%#v err=%v", first, err)
	}
	now = now.Add(time.Minute)
	second, err := store.Fetch(ctx, consumer, 1)
	if err != nil || len(second) != 1 {
		t.Fatalf("second fetch=%#v err=%v", second, err)
	}
	if second[0].Attempts != first[0].Attempts+1 {
		t.Fatalf("attempts first=%d second=%d", first[0].Attempts, second[0].Attempts)
	}

	if err := store.FailAttempt(ctx, consumer, envelope.ID, first[0].Attempts, errors.New("stale")); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale fail error=%v, want ErrLeaseLost", err)
	}
	if err := store.AckAttempt(ctx, consumer, envelope.ID, first[0].Attempts); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale ack error=%v, want ErrLeaseLost", err)
	}
	if err := store.Ack(ctx, consumer, envelope.ID); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("legacy retry ack error=%v, want ErrLeaseLost", err)
	}
	if err := store.Fail(ctx, consumer, envelope.ID, errors.New("legacy stale failure")); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("legacy retry fail error=%v, want ErrLeaseLost", err)
	}
	if err := store.AckAttempt(ctx, consumer, envelope.ID, second[0].Attempts); err != nil {
		t.Fatal(err)
	}
	if err := store.FailAttempt(ctx, consumer, envelope.ID, first[0].Attempts, errors.New("too late")); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("post-ack stale fail error=%v, want ErrLeaseLost", err)
	}

	var row outboxDeliveryRow
	if err := database.GORM().First(&row, "event_id = ? AND consumer = ?", envelope.ID, consumer).Error; err != nil {
		t.Fatal(err)
	}
	if Status(row.Status) != StatusDelivered || row.Attempts != second[0].Attempts {
		t.Fatalf("delivery row=%#v", row)
	}
}

func TestGORMStoreBatchRenewAttemptsReportsFencedLoss(t *testing.T) {
	database := openDeliveryTestDatabase(t)
	defer closeDeliveryTestDatabase(t, database)
	now := time.Date(2026, time.July, 23, 14, 0, 0, 0, time.UTC)
	store := NewGORMStore(database).WithLease(time.Minute)
	store.now = func() time.Time { return now }
	ctx := context.Background()
	consumer := "batch-renew"
	if err := store.Append(ctx, testEnvelope("batch-live"), testEnvelope("batch-stale")); err != nil {
		t.Fatal(err)
	}
	records, err := store.Fetch(ctx, consumer, 2)
	if err != nil || len(records) != 2 {
		t.Fatalf("fetch records=%#v err=%v", records, err)
	}

	results, err := store.RenewAttempts(ctx, []LeaseAttempt{
		{Consumer: consumer, EventID: records[0].Envelope.ID, Attempt: records[0].Attempts},
		{Consumer: consumer, EventID: records[1].Envelope.ID, Attempt: records[1].Attempts + 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("renewal results=%#v", results)
	}
	if results[0].Lost || results[0].Remaining <= 0 || results[0].Remaining > time.Minute {
		t.Fatalf("live renewal=%#v", results[0])
	}
	if !results[1].Lost || results[1].Remaining != 0 {
		t.Fatalf("stale renewal=%#v", results[1])
	}
}

func TestGORMStoreBatchRenewMarksExpiredDuringSlowBatchLost(t *testing.T) {
	database := openDeliveryTestDatabase(t)
	defer closeDeliveryTestDatabase(t, database)
	const lease = 25 * time.Millisecond
	store := NewGORMStore(database).WithLease(lease)
	ctx := context.Background()
	consumer := "slow-batch-renew"
	if err := store.Append(ctx, testEnvelope("slow-one"), testEnvelope("slow-two")); err != nil {
		t.Fatal(err)
	}
	records, err := store.Fetch(ctx, consumer, 2)
	if err != nil || len(records) != 2 {
		t.Fatalf("fetch records=%#v err=%v", records, err)
	}
	if err := database.GORM().Callback().Update().Before("gorm:update").Register(
		"test:slow-batch-renewal",
		func(*gorm.DB) { time.Sleep(35 * time.Millisecond) },
	); err != nil {
		t.Fatal(err)
	}
	attempts := []LeaseAttempt{
		{Consumer: consumer, EventID: records[0].Envelope.ID, Attempt: records[0].Attempts},
		{Consumer: consumer, EventID: records[1].Envelope.ID, Attempt: records[1].Attempts},
	}
	results, err := store.RenewAttempts(ctx, attempts)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || !results[0].Lost || !results[1].Lost {
		t.Fatalf("slow renewal results=%#v, want both lost", results)
	}
}

func TestGORMStoreDeadLettersReplayPreservesAuditAndRejectsActiveLease(t *testing.T) {
	database := openDeliveryTestDatabase(t)
	defer closeDeliveryTestDatabase(t, database)
	now := time.Date(2026, time.July, 23, 15, 0, 0, 0, time.UTC)
	store := NewGORMStore(database).WithRetry(1, 0).WithLease(time.Minute)
	store.now = func() time.Time { return now }
	ctx := context.Background()
	consumer := "dead-letter-admin"
	if err := store.Append(ctx, testEnvelope("dead-replay"), testEnvelope("active-no-replay")); err != nil {
		t.Fatal(err)
	}
	records, err := store.Fetch(ctx, consumer, 2)
	if err != nil || len(records) != 2 {
		t.Fatalf("fetch records=%#v err=%v", records, err)
	}
	byID := make(map[string]Record, len(records))
	for _, record := range records {
		byID[record.Envelope.ID] = record
	}
	if err := store.FailAttempt(ctx, consumer, "dead-replay", byID["dead-replay"].Attempts, errors.New("poison payload")); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplayDeadLetter(ctx, consumer, "active-no-replay"); !errors.Is(err, ErrNotDead) {
		t.Fatalf("active replay error=%v, want ErrNotDead", err)
	}
	letters, err := store.DeadLetters(ctx, consumer, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(letters) != 1 || letters[0].Record.Attempts != 1 || letters[0].Record.LastError != "poison payload" {
		t.Fatalf("dead letters=%#v", letters)
	}
	if err := store.ReplayDeadLetter(ctx, consumer, "dead-replay"); err != nil {
		t.Fatal(err)
	}
	var row outboxDeliveryRow
	if err := database.GORM().First(&row, "event_id = ? AND consumer = ?", "dead-replay", consumer).Error; err != nil {
		t.Fatal(err)
	}
	if Status(row.Status) != StatusPending || row.Attempts != 1 || row.LastError != "" || row.ReplayCount != 1 || row.LastDeadAttempts != 1 || row.LastDeadError != "poison payload" || row.LastReplayedAt == nil {
		t.Fatalf("replayed row=%#v", row)
	}
	replayed, err := store.Fetch(ctx, consumer, 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, record := range replayed {
		if record.Envelope.ID == "dead-replay" {
			found = true
			if record.Attempts != 2 {
				t.Fatalf("replayed attempt=%d, want monotonic attempt 2", record.Attempts)
			}
			if _, err := store.RenewAttempt(ctx, consumer, record.Envelope.ID, 1); !errors.Is(err, ErrLeaseLost) {
				t.Fatalf("stale RenewAttempt() error=%v, want %v", err, ErrLeaseLost)
			}
			if err := store.AckAttempt(ctx, consumer, record.Envelope.ID, 1); !errors.Is(err, ErrLeaseLost) {
				t.Fatalf("stale AckAttempt() error=%v, want %v", err, ErrLeaseLost)
			}
			if err := store.FailAttempt(ctx, consumer, record.Envelope.ID, 1, errors.New("stale")); !errors.Is(err, ErrLeaseLost) {
				t.Fatalf("stale FailAttempt() error=%v, want %v", err, ErrLeaseLost)
			}
		}
	}
	if !found {
		t.Fatalf("replayed delivery was not fetched: %#v", replayed)
	}
}

func TestGORMStoreConcurrentDeadLetterReplayHasSingleWinner(t *testing.T) {
	database := openDeliveryTestDatabase(t)
	defer closeDeliveryTestDatabase(t, database)
	store := NewGORMStore(database).WithRetry(1, 0)
	ctx := context.Background()
	consumer := "concurrent-replay"
	if err := store.Append(ctx, testEnvelope("replay-once")); err != nil {
		t.Fatal(err)
	}
	records, err := store.Fetch(ctx, consumer, 1)
	if err != nil || len(records) != 1 {
		t.Fatalf("fetch records=%#v err=%v", records, err)
	}
	if err := store.FailAttempt(ctx, consumer, "replay-once", records[0].Attempts, errors.New("dead")); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			results <- store.ReplayDeadLetter(ctx, consumer, "replay-once")
		}()
	}
	close(start)
	var succeeded, rejected int
	for i := 0; i < 2; i++ {
		err := <-results
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrNotDead):
			rejected++
		default:
			t.Fatalf("unexpected replay error: %v", err)
		}
	}
	if succeeded != 1 || rejected != 1 {
		t.Fatalf("replay results succeeded=%d rejected=%d", succeeded, rejected)
	}
}

func TestGORMStoreBacklogCountsVirtualAndMaterializedStates(t *testing.T) {
	database := openDeliveryTestDatabase(t)
	defer closeDeliveryTestDatabase(t, database)
	now := time.Date(2026, time.July, 23, 16, 0, 0, 0, time.UTC)
	store := NewGORMStore(database).WithRetry(3, time.Hour).WithLease(time.Minute)
	store.now = func() time.Time { return now }
	ctx := context.Background()
	consumer := "backlog-stats"
	for i := 1; i <= 5; i++ {
		if err := store.Append(ctx, testEnvelope(fmt.Sprintf("stats-%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	initial, err := store.Backlog(ctx, consumer)
	if err != nil {
		t.Fatal(err)
	}
	if initial.Pending != 5 || initial.Inflight != 0 || initial.Failed != 0 || initial.Dead != 0 {
		t.Fatalf("initial backlog=%#v", initial)
	}
	records, err := store.Fetch(ctx, consumer, 4)
	if err != nil || len(records) != 4 {
		t.Fatalf("fetch records=%#v err=%v", records, err)
	}
	if err := store.AckAttempt(ctx, consumer, records[0].Envelope.ID, records[0].Attempts); err != nil {
		t.Fatal(err)
	}
	if err := store.FailAttempt(ctx, consumer, records[1].Envelope.ID, records[1].Attempts, errors.New("retry")); err != nil {
		t.Fatal(err)
	}
	store.maxAttempts = 1
	if err := store.FailAttempt(ctx, consumer, records[2].Envelope.ID, records[2].Attempts, errors.New("dead")); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(ctx, testEnvelope("stats-6")); err != nil {
		t.Fatal(err)
	}
	stats, err := store.Backlog(ctx, consumer)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Pending != 2 || stats.Inflight != 1 || stats.Failed != 1 || stats.Dead != 1 {
		t.Fatalf("backlog=%#v", stats)
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
