// File: eventing/delivery/gorm_store.go
// Purpose: Provides a GORM-backed outbox delivery store.

package delivery

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/saramand9/kpro/database/common/lifecycle"
	gormstore "github.com/saramand9/kpro/database/gorm"
	"github.com/saramand9/kpro/eventing/events"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	outboxEventsTable     = "event_outbox_events"
	outboxDeliveriesTable = "event_outbox_deliveries"
	defaultBatchSize      = 100
	defaultMaxAttempts    = 5
	defaultRetryDelay     = 30 * time.Second
	defaultLeaseDuration  = 30 * time.Second
	minimumLeaseDuration  = time.Millisecond
	renewalChunkSize      = 200
)

type outboxEventRow struct {
	ID            string    `gorm:"column:id;primaryKey;size:128"`
	Type          string    `gorm:"column:type;size:255;not null"`
	AggregateID   string    `gorm:"column:aggregate_id;size:255;not null"`
	TenantID      string    `gorm:"column:tenant_id;size:255"`
	ActorID       string    `gorm:"column:actor_id;size:255"`
	TraceID       string    `gorm:"column:trace_id;size:255"`
	SchemaVersion int       `gorm:"column:schema_version;not null"`
	OccurredAt    time.Time `gorm:"column:occurred_at;not null"`
	Payload       []byte    `gorm:"column:payload;not null"`
	CreatedAt     time.Time `gorm:"column:created_at;not null"`
}

func (outboxEventRow) TableName() string { return outboxEventsTable }

type outboxDeliveryRow struct {
	EventID          string     `gorm:"column:event_id;primaryKey;size:128"`
	Consumer         string     `gorm:"column:consumer;primaryKey;size:255"`
	Status           string     `gorm:"column:status;size:32;not null"`
	Attempts         int        `gorm:"column:attempts;not null"`
	AvailableAt      time.Time  `gorm:"column:available_at;not null"`
	LastError        string     `gorm:"column:last_error;type:text;not null"`
	ReplayCount      int        `gorm:"column:replay_count;not null"`
	LastDeadAttempts int        `gorm:"column:last_dead_attempts;not null"`
	LastDeadError    string     `gorm:"column:last_dead_error;type:text;not null"`
	LastReplayedAt   *time.Time `gorm:"column:last_replayed_at"`
	UpdatedAt        time.Time  `gorm:"column:updated_at;not null"`
}

func (outboxDeliveryRow) TableName() string { return outboxDeliveriesTable }

type joinedOutboxRow struct {
	ID            string
	Type          string
	AggregateID   string
	TenantID      string
	ActorID       string
	TraceID       string
	SchemaVersion int
	OccurredAt    time.Time
	Payload       []byte
	Attempts      int
	LastError     string
}

type joinedDeadLetterRow struct {
	ID               string
	Type             string
	AggregateID      string
	TenantID         string
	ActorID          string
	TraceID          string
	SchemaVersion    int
	OccurredAt       time.Time
	Payload          []byte
	Attempts         int
	LastError        string
	AvailableAt      time.Time
	ReplayCount      int
	LastDeadAttempts int
	LastDeadError    string
	LastReplayedAt   *time.Time
}

type leaseIdentityRow struct {
	EventID  string
	Consumer string
	Attempts int
}

// GORMStore persists event envelopes and per-consumer delivery state.
type GORMStore struct {
	db            *gorm.DB
	maxAttempts   int
	retryDelay    time.Duration
	leaseDuration time.Duration
	now           func() time.Time
}

var _ Store = (*GORMStore)(nil)
var _ AttemptStore = (*GORMStore)(nil)
var _ RenewableAttemptStore = (*GORMStore)(nil)
var _ BatchRenewableAttemptStore = (*GORMStore)(nil)
var _ DeadLetterStore = (*GORMStore)(nil)
var _ BacklogStore = (*GORMStore)(nil)

// InitGORM applies the outbox schema migrations.
func InitGORM(ctx context.Context, database *gormstore.Database) error {
	return gormstore.ApplyMigrations(ctx, database, Migrations())
}

// Migrations returns versioned outbox schema migrations.
func Migrations() []gormstore.Migration {
	return []gormstore.Migration{
		{
			Version: 202607080201,
			Name:    "create_event_outbox",
			Statements: []gormstore.Statement{
				gormstore.DialectSQL(
					`CREATE TABLE IF NOT EXISTS event_outbox_events (
id VARCHAR(128) PRIMARY KEY,
type VARCHAR(255) NOT NULL,
aggregate_id VARCHAR(255) NOT NULL,
tenant_id VARCHAR(255) NOT NULL DEFAULT '',
actor_id VARCHAR(255) NOT NULL DEFAULT '',
trace_id VARCHAR(255) NOT NULL DEFAULT '',
schema_version INTEGER NOT NULL,
occurred_at TIMESTAMPTZ NOT NULL,
payload BYTEA NOT NULL,
created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
)`,
					map[string]string{
						"sqlite": `CREATE TABLE IF NOT EXISTS event_outbox_events (
id TEXT PRIMARY KEY,
type TEXT NOT NULL,
aggregate_id TEXT NOT NULL,
tenant_id TEXT NOT NULL DEFAULT '',
actor_id TEXT NOT NULL DEFAULT '',
trace_id TEXT NOT NULL DEFAULT '',
schema_version INTEGER NOT NULL,
occurred_at DATETIME NOT NULL,
payload BLOB NOT NULL,
created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
)`,
						"mysql": `CREATE TABLE IF NOT EXISTS event_outbox_events (
id VARCHAR(128) PRIMARY KEY,
type VARCHAR(255) NOT NULL,
aggregate_id VARCHAR(255) NOT NULL,
tenant_id VARCHAR(255) NOT NULL DEFAULT '',
actor_id VARCHAR(255) NOT NULL DEFAULT '',
trace_id VARCHAR(255) NOT NULL DEFAULT '',
schema_version INT NOT NULL,
occurred_at DATETIME(3) NOT NULL,
payload LONGBLOB NOT NULL,
created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
)`,
					},
				),
				gormstore.DialectSQL(
					`CREATE TABLE IF NOT EXISTS event_outbox_deliveries (
event_id VARCHAR(128) NOT NULL,
consumer VARCHAR(255) NOT NULL,
status VARCHAR(32) NOT NULL,
attempts INTEGER NOT NULL DEFAULT 0,
available_at TIMESTAMPTZ NOT NULL,
last_error TEXT NOT NULL DEFAULT '',
updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
PRIMARY KEY (event_id, consumer)
)`,
					map[string]string{
						"sqlite": `CREATE TABLE IF NOT EXISTS event_outbox_deliveries (
event_id TEXT NOT NULL,
consumer TEXT NOT NULL,
status TEXT NOT NULL,
attempts INTEGER NOT NULL DEFAULT 0,
available_at DATETIME NOT NULL,
last_error TEXT NOT NULL DEFAULT '',
updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
PRIMARY KEY (event_id, consumer)
)`,
						"mysql": `CREATE TABLE IF NOT EXISTS event_outbox_deliveries (
event_id VARCHAR(128) NOT NULL,
consumer VARCHAR(255) NOT NULL,
status VARCHAR(32) NOT NULL,
attempts INT NOT NULL DEFAULT 0,
available_at DATETIME(3) NOT NULL,
last_error TEXT NOT NULL,
updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
PRIMARY KEY (event_id, consumer)
)`,
					},
				),
				gormstore.DialectSQL(`CREATE INDEX IF NOT EXISTS idx_event_outbox_deliveries_fetch ON event_outbox_deliveries (consumer, status, available_at)`, map[string]string{"mysql": `CREATE INDEX idx_event_outbox_deliveries_fetch ON event_outbox_deliveries (consumer, status, available_at)`}),
			},
		},
		{
			Version: 202607230201,
			Name:    "add_event_outbox_replay_audit",
			Statements: []gormstore.Statement{
				gormstore.SQL(`ALTER TABLE event_outbox_deliveries ADD COLUMN replay_count INTEGER NOT NULL DEFAULT 0`),
				gormstore.SQL(`ALTER TABLE event_outbox_deliveries ADD COLUMN last_dead_attempts INTEGER NOT NULL DEFAULT 0`),
				gormstore.DialectSQL(
					`ALTER TABLE event_outbox_deliveries ADD COLUMN last_dead_error TEXT NOT NULL DEFAULT ''`,
					map[string]string{"mysql": `ALTER TABLE event_outbox_deliveries ADD COLUMN last_dead_error TEXT NOT NULL DEFAULT ('')`},
				),
				gormstore.DialectSQL(
					`ALTER TABLE event_outbox_deliveries ADD COLUMN last_replayed_at TIMESTAMPTZ NULL`,
					map[string]string{
						"sqlite": `ALTER TABLE event_outbox_deliveries ADD COLUMN last_replayed_at DATETIME NULL`,
						"mysql":  `ALTER TABLE event_outbox_deliveries ADD COLUMN last_replayed_at DATETIME(3) NULL`,
					},
				),
			},
		},
		{
			Version: 202607240101,
			Name:    "index_event_outbox_order",
			Statements: []gormstore.Statement{
				gormstore.DialectSQL(
					`CREATE INDEX IF NOT EXISTS idx_event_outbox_events_order ON event_outbox_events (occurred_at, id)`,
					map[string]string{"mysql": `CREATE INDEX idx_event_outbox_events_order ON event_outbox_events (occurred_at, id)`},
				),
			},
		},
	}
}

// NewGORMStore creates a delivery store backed by GORM.
func NewGORMStore(database *gormstore.Database) *GORMStore {
	var db *gorm.DB
	if database != nil {
		db = database.GORM()
	}
	return &GORMStore{
		db:            db,
		maxAttempts:   defaultMaxAttempts,
		retryDelay:    defaultRetryDelay,
		leaseDuration: defaultLeaseDuration,
	}
}

// WithRetry configures failed-record retry behavior.
func (s *GORMStore) WithRetry(maxAttempts int, retryDelay time.Duration) *GORMStore {
	if maxAttempts > 0 {
		s.maxAttempts = maxAttempts
	}
	if retryDelay >= 0 {
		s.retryDelay = retryDelay
	}
	return s
}

// WithLease sets how long a fetched record remains invisible to other workers.
// An inflight record whose lease expires is eligible to be fetched again.
func (s *GORMStore) WithLease(duration time.Duration) *GORMStore {
	if duration > 0 {
		if duration < minimumLeaseDuration {
			duration = minimumLeaseDuration
		}
		s.leaseDuration = duration
	}
	return s
}

// Append persists event envelopes once. Per-consumer rows are created lazily by Fetch.
func (s *GORMStore) Append(ctx context.Context, envelopes ...events.Envelope) error {
	if len(envelopes) == 0 {
		return nil
	}
	db, err := s.gormDB()
	if err != nil {
		return err
	}
	return s.append(ctx, db, envelopes...)
}

// AppendTx persists event envelopes using the caller-owned GORM transaction.
// AppendTx never commits or rolls back tx; callers should use gorm.DB.Transaction
// (or manage Begin/Commit/Rollback themselves) to atomically write domain state
// and its outbox events.
func (s *GORMStore) AppendTx(ctx context.Context, tx *gorm.DB, envelopes ...events.Envelope) error {
	if tx == nil {
		return fmt.Errorf("delivery gorm transaction is nil")
	}
	return s.append(ctx, tx, envelopes...)
}

func (s *GORMStore) append(ctx context.Context, db *gorm.DB, envelopes ...events.Envelope) error {
	if len(envelopes) == 0 {
		return nil
	}
	rows := make([]outboxEventRow, 0, len(envelopes))
	now := s.eventTime()
	for _, envelope := range envelopes {
		if envelope.ID == "" {
			return fmt.Errorf("event envelope id is required")
		}
		occurredAt := envelope.OccurredAt
		if occurredAt.IsZero() {
			occurredAt = now
		}
		rows = append(rows, outboxEventRow{
			ID:            envelope.ID,
			Type:          envelope.Type,
			AggregateID:   envelope.AggregateID,
			TenantID:      envelope.TenantID,
			ActorID:       envelope.ActorID,
			TraceID:       envelope.TraceID,
			SchemaVersion: envelope.SchemaVersion,
			OccurredAt:    occurredAt,
			Payload:       envelope.Payload,
			CreatedAt:     now,
		})
	}
	return db.WithContext(contextOrBackground(ctx)).Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error
}

// Fetch returns available events for one consumer and marks them inflight until
// their lease expires. Expired inflight records are reclaimed automatically.
func (s *GORMStore) Fetch(ctx context.Context, consumer string, limit int) ([]Record, error) {
	if consumer == "" {
		return nil, fmt.Errorf("consumer is required")
	}
	if limit <= 0 {
		limit = defaultBatchSize
	}
	db, err := s.gormDB()
	if err != nil {
		return nil, err
	}
	var records []Record
	err = db.WithContext(contextOrBackground(ctx)).Transaction(func(tx *gorm.DB) error {
		now, err := s.leaseNow(ctx, tx)
		if err != nil {
			return err
		}
		leaseUntil := now.Add(s.leaseDuration)
		if err := ensureConsumerDeliveries(tx, consumer, now, limit); err != nil {
			return err
		}
		if err := tx.Model(&outboxDeliveryRow{}).
			Where("consumer = ? AND status = ? AND available_at <= ? AND (attempts - last_dead_attempts) >= ?", consumer, StatusInflight, now, s.maxAttempts).
			Updates(map[string]any{
				"status":       StatusDead,
				"available_at": now,
				"last_error":   "delivery lease expired after maximum attempts",
				"updated_at":   now,
			}).Error; err != nil {
			return err
		}
		var rows []joinedOutboxRow
		if err := tx.Raw(`SELECT e.id, e.type, e.aggregate_id, e.tenant_id, e.actor_id, e.trace_id, e.schema_version, e.occurred_at, e.payload,
       d.attempts, d.last_error
FROM event_outbox_events e
JOIN event_outbox_deliveries d ON d.event_id = e.id
WHERE d.consumer = ? AND d.status IN (?, ?, ?) AND d.available_at <= ? AND (d.attempts - d.last_dead_attempts) < ?
ORDER BY e.occurred_at ASC, e.id ASC
LIMIT ?`, consumer, StatusPending, StatusFailed, StatusInflight, now, s.maxAttempts, limit).Scan(&rows).Error; err != nil {
			return err
		}
		records = make([]Record, 0, len(rows))
		for _, row := range rows {
			attempts := row.Attempts + 1
			result := tx.Model(&outboxDeliveryRow{}).
				Where("event_id = ? AND consumer = ?", row.ID, consumer).
				Where("status IN ? AND available_at <= ? AND attempts = ?", []string{string(StatusPending), string(StatusFailed), string(StatusInflight)}, now, row.Attempts).
				Updates(map[string]any{
					"status":       StatusInflight,
					"attempts":     gorm.Expr("attempts + 1"),
					"available_at": leaseUntil,
					"updated_at":   now,
				})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				continue
			}
			records = append(records, Record{
				Envelope: events.Envelope{
					ID:            row.ID,
					Type:          row.Type,
					AggregateID:   row.AggregateID,
					TenantID:      row.TenantID,
					ActorID:       row.ActorID,
					TraceID:       row.TraceID,
					SchemaVersion: row.SchemaVersion,
					OccurredAt:    row.OccurredAt,
					Payload:       row.Payload,
				},
				Consumer:    consumer,
				Status:      StatusInflight,
				Attempts:    attempts,
				AvailableAt: leaseUntil,
				LastError:   row.LastError,
			})
		}
		return nil
	})
	return records, err
}

// Ack marks a first-attempt event delivered for compatibility with Store.
// Deprecated: callers of a leased GORMStore should use AckAttempt. Once a
// record has been retried, Ack returns ErrLeaseLost because it cannot identify
// which worker owns the current attempt.
func (s *GORMStore) Ack(ctx context.Context, consumer string, eventID string) error {
	db, err := s.gormDB()
	if err != nil {
		return err
	}
	now, err := s.leaseNow(ctx, db)
	if err != nil {
		return err
	}
	result := db.WithContext(contextOrBackground(ctx)).Model(&outboxDeliveryRow{}).
		Where("event_id = ? AND consumer = ? AND status = ? AND attempts = 1 AND available_at > ?", eventID, consumer, StatusInflight, now).
		Updates(map[string]any{"status": StatusDelivered, "last_error": "", "updated_at": now})
	return settleResult(result, consumer, eventID, 1)
}

// AckAttempt marks the event delivered only if attempt still owns a live lease.
func (s *GORMStore) AckAttempt(ctx context.Context, consumer string, eventID string, attempt int) error {
	if attempt <= 0 {
		return fmt.Errorf("delivery attempt must be positive")
	}
	db, err := s.gormDB()
	if err != nil {
		return err
	}
	now, err := s.leaseNow(ctx, db)
	if err != nil {
		return err
	}
	result := db.WithContext(contextOrBackground(ctx)).Model(&outboxDeliveryRow{}).
		Where("event_id = ? AND consumer = ? AND status = ? AND attempts = ? AND available_at > ?", eventID, consumer, StatusInflight, attempt, now).
		Updates(map[string]any{"status": StatusDelivered, "last_error": "", "updated_at": now})
	return settleResult(result, consumer, eventID, attempt)
}

// RenewAttempt extends a live lease without changing its fenced attempt and
// returns the duration the caller should use for its next renewal timer.
func (s *GORMStore) RenewAttempt(ctx context.Context, consumer string, eventID string, attempt int) (time.Duration, error) {
	lease := LeaseAttempt{Consumer: consumer, EventID: eventID, Attempt: attempt}
	results, err := s.RenewAttempts(ctx, []LeaseAttempt{lease})
	if err != nil {
		return 0, err
	}
	if len(results) != 1 || results[0].Lost {
		return 0, fmt.Errorf("%w: consumer %q event %q attempt %d", ErrLeaseLost, consumer, eventID, attempt)
	}
	return results[0].Remaining, nil
}

// RenewAttempts renews fenced leases against one database-clock sample. It
// updates bounded chunks and then classifies each fenced attempt, so one stale
// item does not fail or hide successful renewals in the same batch.
func (s *GORMStore) RenewAttempts(ctx context.Context, attempts []LeaseAttempt) ([]LeaseRenewal, error) {
	if len(attempts) == 0 {
		return []LeaseRenewal{}, nil
	}
	for _, attempt := range attempts {
		if attempt.Consumer == "" {
			return nil, fmt.Errorf("delivery consumer is required")
		}
		if attempt.EventID == "" {
			return nil, fmt.Errorf("delivery event id is required")
		}
		if attempt.Attempt <= 0 {
			return nil, fmt.Errorf("delivery attempt must be positive")
		}
	}
	db, err := s.gormDB()
	if err != nil {
		return nil, err
	}
	startedAt := time.Now()
	results := make([]LeaseRenewal, len(attempts))
	for i, attempt := range attempts {
		results[i].LeaseAttempt = attempt
	}
	err = db.WithContext(contextOrBackground(ctx)).Transaction(func(tx *gorm.DB) error {
		now, err := s.leaseNow(ctx, tx)
		if err != nil {
			return err
		}
		leaseUntil := now.Add(s.leaseDuration)
		for offset := 0; offset < len(attempts); offset += renewalChunkSize {
			end := offset + renewalChunkSize
			if end > len(attempts) {
				end = len(attempts)
			}
			chunk := attempts[offset:end]
			predicate, args := leaseAttemptPredicate(chunk)
			result := tx.Model(&outboxDeliveryRow{}).
				Where("status = ? AND available_at > ?", StatusInflight, now).
				Where(predicate, args...).
				Updates(map[string]any{"available_at": leaseUntil, "updated_at": now})
			if result.Error != nil {
				return result.Error
			}
			var live []leaseIdentityRow
			if err := tx.Model(&outboxDeliveryRow{}).
				Select("event_id", "consumer", "attempts").
				Where("status = ? AND available_at > ?", StatusInflight, now).
				Where(predicate, args...).
				Scan(&live).Error; err != nil {
				return err
			}
			liveSet := make(map[LeaseAttempt]struct{}, len(live))
			for _, row := range live {
				liveSet[LeaseAttempt{Consumer: row.Consumer, EventID: row.EventID, Attempt: row.Attempts}] = struct{}{}
			}
			for i, attempt := range chunk {
				_, ok := liveSet[attempt]
				results[offset+i].Lost = !ok
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	remaining := s.leaseDuration - time.Since(startedAt)
	for i := range results {
		if results[i].Lost || remaining <= 0 {
			results[i].Lost = true
			continue
		}
		results[i].Remaining = remaining
	}
	return results, nil
}

func leaseAttemptPredicate(attempts []LeaseAttempt) (string, []any) {
	parts := make([]string, len(attempts))
	args := make([]any, 0, len(attempts)*3)
	for i, attempt := range attempts {
		parts[i] = "(event_id = ? AND consumer = ? AND attempts = ?)"
		args = append(args, attempt.EventID, attempt.Consumer, attempt.Attempt)
	}
	return "(" + strings.Join(parts, " OR ") + ")", args
}

// Fail settles only the first attempt for compatibility with Store.
// Deprecated: callers of a leased GORMStore should use FailAttempt so a stale
// worker cannot fail a newer attempt.
func (s *GORMStore) Fail(ctx context.Context, consumer string, eventID string, cause error) error {
	db, err := s.gormDB()
	if err != nil {
		return err
	}
	var row outboxDeliveryRow
	if err := db.WithContext(contextOrBackground(ctx)).First(&row, "event_id = ? AND consumer = ?", eventID, consumer).Error; err != nil {
		return err
	}
	if row.Attempts != 1 {
		return fmt.Errorf("%w: consumer %q event %q requires attempt-aware settlement", ErrLeaseLost, consumer, eventID)
	}
	now, err := s.leaseNow(ctx, db)
	if err != nil {
		return err
	}
	status := StatusFailed
	availableAt := now.Add(s.retryDelay)
	if row.Attempts-row.LastDeadAttempts >= s.maxAttempts {
		status = StatusDead
		availableAt = now
	}
	message := ""
	if cause != nil {
		message = cause.Error()
	}
	result := db.WithContext(contextOrBackground(ctx)).Model(&outboxDeliveryRow{}).
		Where("event_id = ? AND consumer = ? AND status = ? AND attempts = 1 AND available_at > ?", eventID, consumer, StatusInflight, now).
		Updates(map[string]any{"status": status, "available_at": availableAt, "last_error": message, "updated_at": now})
	return settleResult(result, consumer, eventID, 1)
}

// FailAttempt schedules a retry only if attempt still owns a live lease.
func (s *GORMStore) FailAttempt(ctx context.Context, consumer string, eventID string, attempt int, cause error) error {
	if attempt <= 0 {
		return fmt.Errorf("delivery attempt must be positive")
	}
	db, err := s.gormDB()
	if err != nil {
		return err
	}
	now, err := s.leaseNow(ctx, db)
	if err != nil {
		return err
	}
	var lease struct {
		LastDeadAttempts int
	}
	if err := db.WithContext(contextOrBackground(ctx)).Model(&outboxDeliveryRow{}).
		Select("last_dead_attempts").
		Where("event_id = ? AND consumer = ?", eventID, consumer).
		Take(&lease).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("%w: consumer %q event %q attempt %d", ErrLeaseLost, consumer, eventID, attempt)
		}
		return err
	}
	status := StatusFailed
	availableAt := now.Add(s.retryDelay)
	if attempt-lease.LastDeadAttempts >= s.maxAttempts {
		status = StatusDead
		availableAt = now
	}
	message := ""
	if cause != nil {
		message = cause.Error()
	}
	result := db.WithContext(contextOrBackground(ctx)).Model(&outboxDeliveryRow{}).
		Where("event_id = ? AND consumer = ? AND status = ? AND attempts = ? AND available_at > ?", eventID, consumer, StatusInflight, attempt, now).
		Updates(map[string]any{"status": status, "available_at": availableAt, "last_error": message, "updated_at": now})
	return settleResult(result, consumer, eventID, attempt)
}

// DeadLetters returns dead deliveries ordered deterministically by their last
// state change. The current Record retains the attempts and failure that made
// the delivery dead.
func (s *GORMStore) DeadLetters(ctx context.Context, consumer string, limit int) ([]DeadLetter, error) {
	if consumer == "" {
		return nil, fmt.Errorf("consumer is required")
	}
	if limit <= 0 {
		limit = defaultBatchSize
	}
	db, err := s.gormDB()
	if err != nil {
		return nil, err
	}
	var rows []joinedDeadLetterRow
	err = db.WithContext(contextOrBackground(ctx)).Raw(`SELECT e.id, e.type, e.aggregate_id, e.tenant_id, e.actor_id, e.trace_id, e.schema_version, e.occurred_at, e.payload,
       d.attempts, d.last_error, d.available_at, d.replay_count, d.last_dead_attempts, d.last_dead_error, d.last_replayed_at
FROM event_outbox_events e
JOIN event_outbox_deliveries d ON d.event_id = e.id
WHERE d.consumer = ? AND d.status = ?
ORDER BY d.updated_at ASC, e.id ASC
LIMIT ?`, consumer, StatusDead, limit).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	letters := make([]DeadLetter, 0, len(rows))
	for _, row := range rows {
		letters = append(letters, DeadLetter{
			Record: Record{
				Envelope: events.Envelope{
					ID:            row.ID,
					Type:          row.Type,
					AggregateID:   row.AggregateID,
					TenantID:      row.TenantID,
					ActorID:       row.ActorID,
					TraceID:       row.TraceID,
					SchemaVersion: row.SchemaVersion,
					OccurredAt:    row.OccurredAt,
					Payload:       row.Payload,
				},
				Consumer:    consumer,
				Status:      StatusDead,
				Attempts:    row.Attempts,
				AvailableAt: row.AvailableAt,
				LastError:   row.LastError,
			},
			ReplayCount:      row.ReplayCount,
			LastDeadAttempts: row.LastDeadAttempts,
			LastDeadError:    row.LastDeadError,
			LastReplayedAt:   row.LastReplayedAt,
		})
	}
	return letters, nil
}

// ReplayDeadLetter atomically moves only a dead delivery back to pending. The
// failed attempt and error are copied to audit fields, which also restart the
// per-replay retry budget. Attempts remain monotonic so a receipt from an old
// replay generation can never fence a newer lease. A concurrent replay, or
// any active lease, returns ErrNotDead.
func (s *GORMStore) ReplayDeadLetter(ctx context.Context, consumer string, eventID string) error {
	if consumer == "" {
		return fmt.Errorf("consumer is required")
	}
	if eventID == "" {
		return fmt.Errorf("event id is required")
	}
	db, err := s.gormDB()
	if err != nil {
		return err
	}
	return db.WithContext(contextOrBackground(ctx)).Transaction(func(tx *gorm.DB) error {
		now, err := s.leaseNow(ctx, tx)
		if err != nil {
			return err
		}
		var dead outboxDeliveryRow
		err = tx.Select("attempts", "last_error", "replay_count").
			Where("event_id = ? AND consumer = ? AND status = ?", eventID, consumer, StatusDead).
			Take(&dead).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: consumer %q event %q", ErrNotDead, consumer, eventID)
			}
			return err
		}
		result := tx.Model(&outboxDeliveryRow{}).
			Where("event_id = ? AND consumer = ? AND status = ? AND attempts = ? AND replay_count = ?", eventID, consumer, StatusDead, dead.Attempts, dead.ReplayCount).
			Updates(map[string]any{
				"status":             StatusPending,
				"available_at":       now,
				"last_error":         "",
				"replay_count":       gorm.Expr("replay_count + 1"),
				"last_dead_attempts": dead.Attempts,
				"last_dead_error":    dead.LastError,
				"last_replayed_at":   now,
				"updated_at":         now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return fmt.Errorf("%w: consumer %q event %q", ErrNotDead, consumer, eventID)
		}
		return nil
	})
}

// Backlog reports delivery state counts in one query. Missing per-consumer
// rows are virtual pending deliveries, matching Fetch's lazy materialization.
// The query counts the narrow delivery index and the event table separately;
// it does not LEFT JOIN and aggregate the complete delivered history.
func (s *GORMStore) Backlog(ctx context.Context, consumer string) (BacklogStats, error) {
	if consumer == "" {
		return BacklogStats{}, fmt.Errorf("consumer is required")
	}
	db, err := s.gormDB()
	if err != nil {
		return BacklogStats{}, err
	}
	var stats BacklogStats
	err = db.WithContext(contextOrBackground(ctx)).Raw(`SELECT
       (SELECT COUNT(*) FROM event_outbox_events) - COUNT(*) + COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0) AS pending,
       COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0) AS inflight,
       COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0) AS failed,
       COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0) AS dead
FROM event_outbox_deliveries
WHERE consumer = ?`, StatusPending, StatusInflight, StatusFailed, StatusDead, consumer).Scan(&stats).Error
	return stats, err
}

func ensureConsumerDeliveries(db *gorm.DB, consumer string, now time.Time, limit int) error {
	const insertBody = ` INTO event_outbox_deliveries (event_id, consumer, status, attempts, available_at, last_error, updated_at)
SELECT e.id, ?, ?, 0, ?, '', ?
FROM event_outbox_events e`
	const missing = ` WHERE NOT EXISTS (
  SELECT 1 FROM event_outbox_deliveries d WHERE d.event_id = e.id AND d.consumer = ?
)
ORDER BY e.occurred_at ASC, e.id ASC
LIMIT ?`
	args := []any{consumer, StatusPending, now, now, consumer, limit}
	switch db.Dialector.Name() {
	case "mysql":
		return db.Exec("INSERT IGNORE"+insertBody+missing, args...).Error
	case "postgres", "sqlite":
		return db.Exec("INSERT"+insertBody+missing+" ON CONFLICT (event_id, consumer) DO NOTHING", args...).Error
	default:
		return db.Exec("INSERT"+insertBody+missing, args...).Error
	}
}

func (s *GORMStore) gormDB() (*gorm.DB, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("%w: delivery gorm store is nil", lifecycle.ErrUnavailable)
	}
	return s.db, nil
}

func (s *GORMStore) eventTime() time.Time {
	if s != nil && s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}

// leaseNow uses the database server's UTC clock for production lease
// decisions. The optional store clock is reserved for deterministic tests.
func (s *GORMStore) leaseNow(ctx context.Context, db *gorm.DB) (time.Time, error) {
	if s != nil && s.now != nil {
		return s.now().UTC(), nil
	}
	if db == nil || db.Dialector == nil {
		return time.Time{}, fmt.Errorf("delivery lease database is nil")
	}
	query := ""
	switch db.Dialector.Name() {
	case "postgres":
		query = "SELECT CAST(EXTRACT(EPOCH FROM clock_timestamp()) * 1000000 AS BIGINT)"
	case "mysql":
		query = "SELECT TIMESTAMPDIFF(MICROSECOND, '1970-01-01 00:00:00', UTC_TIMESTAMP(6))"
	case "sqlite":
		query = "SELECT CAST((julianday('now') - 2440587.5) * 86400000000 AS INTEGER)"
	default:
		return time.Time{}, fmt.Errorf("delivery lease clock does not support dialect %q", db.Dialector.Name())
	}
	var micros int64
	if err := db.WithContext(contextOrBackground(ctx)).Raw(query).Scan(&micros).Error; err != nil {
		return time.Time{}, fmt.Errorf("read %s delivery lease clock: %w", db.Dialector.Name(), err)
	}
	return time.UnixMicro(micros).UTC(), nil
}

func settleResult(result *gorm.DB, consumer, eventID string, attempt int) error {
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("%w: consumer %q event %q attempt %d", ErrLeaseLost, consumer, eventID, attempt)
	}
	return nil
}

func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
