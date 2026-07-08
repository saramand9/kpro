// File: eventing/delivery/gorm_store.go
// Purpose: Provides a GORM-backed outbox delivery store.

package delivery

import (
	"context"
	"fmt"
	"time"

	"github.com/saramand9/bignet/kpro/database/common/lifecycle"
	gormstore "github.com/saramand9/bignet/kpro/database/gorm"
	"github.com/saramand9/bignet/kpro/eventing/events"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	outboxEventsTable     = "event_outbox_events"
	outboxDeliveriesTable = "event_outbox_deliveries"
	defaultBatchSize      = 100
	defaultMaxAttempts    = 5
	defaultRetryDelay     = 30 * time.Second
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
	EventID     string    `gorm:"column:event_id;primaryKey;size:128"`
	Consumer    string    `gorm:"column:consumer;primaryKey;size:255"`
	Status      string    `gorm:"column:status;size:32;not null"`
	Attempts    int       `gorm:"column:attempts;not null"`
	AvailableAt time.Time `gorm:"column:available_at;not null"`
	LastError   string    `gorm:"column:last_error;type:text;not null"`
	UpdatedAt   time.Time `gorm:"column:updated_at;not null"`
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
	Consumer      string
	Status        string
	Attempts      int
	AvailableAt   time.Time
	LastError     string
}

// GORMStore persists event envelopes and per-consumer delivery state.
type GORMStore struct {
	db          *gorm.DB
	maxAttempts int
	retryDelay  time.Duration
}

var _ Store = (*GORMStore)(nil)

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
	}
}

// NewGORMStore creates a delivery store backed by GORM.
func NewGORMStore(database *gormstore.Database) *GORMStore {
	var db *gorm.DB
	if database != nil {
		db = database.GORM()
	}
	return &GORMStore{db: db, maxAttempts: defaultMaxAttempts, retryDelay: defaultRetryDelay}
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

// Append persists event envelopes once. Per-consumer rows are created lazily by Fetch.
func (s *GORMStore) Append(ctx context.Context, envelopes ...events.Envelope) error {
	if len(envelopes) == 0 {
		return nil
	}
	db, err := s.gormDB()
	if err != nil {
		return err
	}
	rows := make([]outboxEventRow, 0, len(envelopes))
	now := time.Now().UTC()
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

// Fetch returns available events for one consumer and marks them inflight.
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
	now := time.Now().UTC()
	var records []Record
	err = db.WithContext(contextOrBackground(ctx)).Transaction(func(tx *gorm.DB) error {
		if err := ensureConsumerDeliveries(tx, consumer, now); err != nil {
			return err
		}
		var rows []joinedOutboxRow
		if err := tx.Raw(`SELECT e.id, e.type, e.aggregate_id, e.tenant_id, e.actor_id, e.trace_id, e.schema_version, e.occurred_at, e.payload,
       d.consumer, d.status, d.attempts, d.available_at, d.last_error
FROM event_outbox_events e
JOIN event_outbox_deliveries d ON d.event_id = e.id
WHERE d.consumer = ? AND d.status IN (?, ?) AND d.available_at <= ?
ORDER BY e.occurred_at ASC, e.id ASC
LIMIT ?`, consumer, StatusPending, StatusFailed, now, limit).Scan(&rows).Error; err != nil {
			return err
		}
		records = make([]Record, 0, len(rows))
		for _, row := range rows {
			attempts := row.Attempts + 1
			result := tx.Model(&outboxDeliveryRow{}).
				Where("event_id = ? AND consumer = ? AND status IN ?", row.ID, consumer, []string{string(StatusPending), string(StatusFailed)}).
				Updates(map[string]any{"status": StatusInflight, "attempts": attempts, "updated_at": now})
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
				AvailableAt: row.AvailableAt,
				LastError:   row.LastError,
			})
		}
		return nil
	})
	return records, err
}

// Ack marks one event delivered for one consumer.
func (s *GORMStore) Ack(ctx context.Context, consumer string, eventID string) error {
	db, err := s.gormDB()
	if err != nil {
		return err
	}
	return db.WithContext(contextOrBackground(ctx)).Model(&outboxDeliveryRow{}).
		Where("event_id = ? AND consumer = ?", eventID, consumer).
		Updates(map[string]any{"status": StatusDelivered, "last_error": "", "updated_at": time.Now().UTC()}).Error
}

// Fail schedules a retry or marks one event dead after too many attempts.
func (s *GORMStore) Fail(ctx context.Context, consumer string, eventID string, cause error) error {
	db, err := s.gormDB()
	if err != nil {
		return err
	}
	var row outboxDeliveryRow
	if err := db.WithContext(contextOrBackground(ctx)).First(&row, "event_id = ? AND consumer = ?", eventID, consumer).Error; err != nil {
		return err
	}
	now := time.Now().UTC()
	status := StatusFailed
	availableAt := now.Add(s.retryDelay)
	if row.Attempts >= s.maxAttempts {
		status = StatusDead
		availableAt = now
	}
	message := ""
	if cause != nil {
		message = cause.Error()
	}
	return db.WithContext(contextOrBackground(ctx)).Model(&outboxDeliveryRow{}).
		Where("event_id = ? AND consumer = ?", eventID, consumer).
		Updates(map[string]any{"status": status, "available_at": availableAt, "last_error": message, "updated_at": now}).Error
}

func ensureConsumerDeliveries(db *gorm.DB, consumer string, now time.Time) error {
	return db.Exec(`INSERT INTO event_outbox_deliveries (event_id, consumer, status, attempts, available_at, last_error, updated_at)
SELECT e.id, ?, ?, 0, ?, '', ?
FROM event_outbox_events e
WHERE NOT EXISTS (
  SELECT 1 FROM event_outbox_deliveries d WHERE d.event_id = e.id AND d.consumer = ?
)`, consumer, StatusPending, now, now, consumer).Error
}

func (s *GORMStore) gormDB() (*gorm.DB, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("%w: delivery gorm store is nil", lifecycle.ErrUnavailable)
	}
	return s.db, nil
}

func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
