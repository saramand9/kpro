package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/saramand9/bignet/kpro/database/dbx"
	"github.com/saramand9/bignet/kpro/database/facts"
	"github.com/saramand9/bignet/kpro/database/outbox"
)

const (
	defaultFactTable     = "outbox_facts"
	defaultDeliveryTable = "outbox_deliveries"
	defaultOutboxLimit   = 100
	defaultMaxAttempts   = 10
	defaultClaimTimeout  = 5 * time.Minute
	outboxTimeLayout     = "2006-01-02T15:04:05.000000000Z"
)

var sqlIdentifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// OutboxConfig controls the SQL outbox table names and retry policy.
type OutboxConfig struct {
	FactTable     string
	DeliveryTable string
	RetryDelay    time.Duration
	ClaimTimeout  time.Duration
	MaxAttempts   int
}

// OutboxStore stores facts in SQL and tracks delivery per projection consumer.
type OutboxStore struct {
	database *dbx.Database
	config   OutboxConfig
}

var _ outbox.Store = (*OutboxStore)(nil)

// NewOutboxStore creates a SQL-backed outbox.
func NewOutboxStore(database *dbx.Database, config OutboxConfig) *OutboxStore {
	config = normalizeOutboxConfig(config)
	return &OutboxStore{database: database, config: config}
}

// Init creates the outbox tables when they do not already exist.
func (s *OutboxStore) Init(ctx context.Context) error {
	if err := s.validate(); err != nil {
		return err
	}
	statements, err := s.schemaSQL()
	if err != nil {
		return err
	}
	for _, statement := range statements {
		if _, err := s.database.DB().ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return s.initIndexes(ctx)
}

// Append persists fact envelopes.
func (s *OutboxStore) Append(ctx context.Context, envelopes ...facts.Envelope) error {
	if err := s.validate(); err != nil {
		return err
	}
	if len(envelopes) == 0 {
		return nil
	}
	factTable, err := s.quote(s.config.FactTable)
	if err != nil {
		return err
	}
	columns := []string{
		"id", "type", "aggregate_id", "tenant_id", "actor_id", "trace_id",
		"schema_version", "occurred_at", "payload", "created_at",
	}
	query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		factTable,
		s.quoteList(columns),
		s.placeholders(1, len(columns)),
	)
	now := outboxTime(time.Now())
	execer := s.execer(ctx)
	for _, envelope := range envelopes {
		if envelope.ID == "" {
			return fmt.Errorf("outbox fact id is required")
		}
		if envelope.Type == "" {
			return fmt.Errorf("outbox fact type is required for %q", envelope.ID)
		}
		if envelope.SchemaVersion == 0 {
			envelope.SchemaVersion = 1
		}
		if envelope.OccurredAt.IsZero() {
			envelope.OccurredAt = time.Now()
		}
		_, err := execer.ExecContext(ctx, query,
			envelope.ID,
			envelope.Type,
			envelope.AggregateID,
			envelope.TenantID,
			envelope.ActorID,
			envelope.TraceID,
			envelope.SchemaVersion,
			outboxTime(envelope.OccurredAt),
			envelope.Payload,
			now,
		)
		if err != nil {
			return err
		}
	}
	return nil
}

// Fetch returns records pending for one projection consumer.
func (s *OutboxStore) Fetch(ctx context.Context, consumer string, limit int) ([]outbox.Record, error) {
	if err := s.validateConsumer(consumer); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = defaultOutboxLimit
	}
	if err := s.ensureDeliveries(ctx, consumer); err != nil {
		return nil, err
	}
	factIDs, err := s.claim(ctx, consumer, limit)
	if err != nil {
		return nil, err
	}
	if len(factIDs) == 0 {
		return nil, nil
	}
	return s.fetchClaimed(ctx, consumer, factIDs)
}

func (s *OutboxStore) claim(ctx context.Context, consumer string, limit int) ([]string, error) {
	factTable, err := s.quote(s.config.FactTable)
	if err != nil {
		return nil, err
	}
	deliveryTable, err := s.quote(s.config.DeliveryTable)
	if err != nil {
		return nil, err
	}
	now := outboxTime(time.Now())
	claimUntil := outboxTime(time.Now().Add(s.config.ClaimTimeout))
	claimed := make([]string, 0, limit)
	err = s.database.Tx(ctx, func(tx *dbx.Tx) error {
		selectQuery := fmt.Sprintf(`SELECT d.%s
FROM %s d
INNER JOIN %s f ON f.%s = d.%s
WHERE d.%s = %s AND (
	(d.%s IN (%s, %s) AND d.%s <= %s)
	OR (d.%s = %s AND d.%s <= %s)
)
ORDER BY f.%s ASC, f.%s ASC
LIMIT %s`,
			s.q("fact_id"),
			deliveryTable,
			factTable,
			s.q("id"), s.q("fact_id"),
			s.q("consumer"), s.placeholder(1),
			s.q("status"), s.placeholder(2), s.placeholder(3),
			s.q("available_at"), s.placeholder(4),
			s.q("status"), s.placeholder(5),
			s.q("available_at"), s.placeholder(6),
			s.q("occurred_at"), s.q("id"),
			s.placeholder(7),
		)
		rows, err := tx.SQLTx().QueryContext(ctx, selectQuery,
			consumer,
			string(outbox.StatusPending),
			string(outbox.StatusFailed),
			now,
			string(outbox.StatusInflight),
			now,
			limit,
		)
		if err != nil {
			return err
		}

		candidates := make([]string, 0, limit)
		for rows.Next() {
			var factID string
			if err := rows.Scan(&factID); err != nil {
				return err
			}
			candidates = append(candidates, factID)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}
		updateQuery := fmt.Sprintf(`UPDATE %s
SET %s=%s, %s=%s, %s=%s
WHERE %s=%s AND %s=%s AND (
	(%s IN (%s, %s) AND %s <= %s)
	OR (%s = %s AND %s <= %s)
)`,
			deliveryTable,
			s.q("status"), s.placeholder(1),
			s.q("available_at"), s.placeholder(2),
			s.q("updated_at"), s.placeholder(3),
			s.q("consumer"), s.placeholder(4),
			s.q("fact_id"), s.placeholder(5),
			s.q("status"), s.placeholder(6), s.placeholder(7),
			s.q("available_at"), s.placeholder(8),
			s.q("status"), s.placeholder(9),
			s.q("available_at"), s.placeholder(10),
		)
		for _, factID := range candidates {
			result, err := tx.ExecContext(ctx, updateQuery,
				string(outbox.StatusInflight),
				claimUntil,
				now,
				consumer,
				factID,
				string(outbox.StatusPending),
				string(outbox.StatusFailed),
				now,
				string(outbox.StatusInflight),
				now,
			)
			if err != nil {
				return err
			}
			rowsAffected, err := result.RowsAffected()
			if err == nil && rowsAffected == 0 {
				continue
			}
			claimed = append(claimed, factID)
		}
		return nil
	})
	return claimed, err
}

func (s *OutboxStore) fetchClaimed(ctx context.Context, consumer string, factIDs []string) ([]outbox.Record, error) {
	factTable, err := s.quote(s.config.FactTable)
	if err != nil {
		return nil, err
	}
	deliveryTable, err := s.quote(s.config.DeliveryTable)
	if err != nil {
		return nil, err
	}
	args := make([]any, 0, len(factIDs)+2)
	args = append(args, consumer, string(outbox.StatusInflight))
	for _, factID := range factIDs {
		args = append(args, factID)
	}
	query := fmt.Sprintf(`SELECT
f.%s, f.%s, f.%s, f.%s, f.%s, f.%s, f.%s, f.%s, f.%s,
d.%s, d.%s, d.%s, d.%s, d.%s
FROM %s d
INNER JOIN %s f ON f.%s = d.%s
WHERE d.%s = %s AND d.%s = %s AND d.%s IN (%s)
ORDER BY f.%s ASC, f.%s ASC`,
		s.q("id"), s.q("type"), s.q("aggregate_id"), s.q("tenant_id"), s.q("actor_id"), s.q("trace_id"),
		s.q("schema_version"), s.q("occurred_at"), s.q("payload"),
		s.q("consumer"), s.q("status"), s.q("attempts"), s.q("available_at"), s.q("last_error"),
		deliveryTable,
		factTable,
		s.q("id"), s.q("fact_id"),
		s.q("consumer"), s.placeholder(1),
		s.q("status"), s.placeholder(2),
		s.q("fact_id"), s.placeholders(3, len(factIDs)),
		s.q("occurred_at"), s.q("id"),
	)
	rows, err := s.database.DB().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	records := make([]outbox.Record, 0)
	for rows.Next() {
		var record outbox.Record
		var occurredAt string
		var availableAt string
		if err := rows.Scan(
			&record.Envelope.ID,
			&record.Envelope.Type,
			&record.Envelope.AggregateID,
			&record.Envelope.TenantID,
			&record.Envelope.ActorID,
			&record.Envelope.TraceID,
			&record.Envelope.SchemaVersion,
			&occurredAt,
			&record.Envelope.Payload,
			&record.Consumer,
			&record.Status,
			&record.Attempts,
			&availableAt,
			&record.LastError,
		); err != nil {
			return nil, err
		}
		record.Envelope.OccurredAt = parseOutboxTime(occurredAt)
		record.AvailableAt = parseOutboxTime(availableAt)
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return records, nil
}

// Ack marks one fact delivered for one consumer.
func (s *OutboxStore) Ack(ctx context.Context, consumer string, factID string) error {
	if err := s.validateConsumerFact(consumer, factID); err != nil {
		return err
	}
	deliveryTable, err := s.quote(s.config.DeliveryTable)
	if err != nil {
		return err
	}
	query := fmt.Sprintf("UPDATE %s SET %s=%s, %s=%s, %s=%s WHERE %s=%s AND %s=%s AND %s=%s",
		deliveryTable,
		s.q("status"), s.placeholder(1),
		s.q("available_at"), s.placeholder(2),
		s.q("updated_at"), s.placeholder(3),
		s.q("consumer"), s.placeholder(4),
		s.q("fact_id"), s.placeholder(5),
		s.q("status"), s.placeholder(6),
	)
	result, err := s.execer(ctx).ExecContext(ctx, query,
		string(outbox.StatusDelivered),
		outboxTime(time.Now()),
		outboxTime(time.Now()),
		consumer,
		factID,
		string(outbox.StatusInflight),
	)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err == nil && rows == 0 {
		status, ok, statusErr := s.deliveryStatus(ctx, consumer, factID)
		if statusErr != nil {
			return statusErr
		}
		if ok && status == outbox.StatusDelivered {
			return nil
		}
		return fmt.Errorf("outbox delivery %q for consumer %q is not inflight", factID, consumer)
	}
	return nil
}

// Fail records a delivery failure and schedules a retry or dead-letter state.
func (s *OutboxStore) Fail(ctx context.Context, consumer string, factID string, failErr error) error {
	if err := s.validateConsumerFact(consumer, factID); err != nil {
		return err
	}
	deliveryTable, err := s.quote(s.config.DeliveryTable)
	if err != nil {
		return err
	}
	currentStatus, attempts, err := s.deliveryState(ctx, consumer, factID)
	if err != nil {
		return err
	}
	if currentStatus != outbox.StatusInflight {
		return fmt.Errorf("outbox delivery %q for consumer %q is not inflight", factID, consumer)
	}
	attempts++
	nextStatus := outbox.StatusFailed
	if attempts >= s.config.MaxAttempts {
		nextStatus = outbox.StatusDead
	}
	message := ""
	if failErr != nil {
		message = failErr.Error()
	}
	query := fmt.Sprintf("UPDATE %s SET %s=%s, %s=%s, %s=%s, %s=%s, %s=%s WHERE %s=%s AND %s=%s AND %s=%s",
		deliveryTable,
		s.q("status"), s.placeholder(1),
		s.q("attempts"), s.placeholder(2),
		s.q("available_at"), s.placeholder(3),
		s.q("last_error"), s.placeholder(4),
		s.q("updated_at"), s.placeholder(5),
		s.q("consumer"), s.placeholder(6),
		s.q("fact_id"), s.placeholder(7),
		s.q("status"), s.placeholder(8),
	)
	result, err := s.execer(ctx).ExecContext(ctx, query,
		string(nextStatus),
		attempts,
		outboxTime(time.Now().Add(s.config.RetryDelay)),
		message,
		outboxTime(time.Now()),
		consumer,
		factID,
		string(outbox.StatusInflight),
	)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err == nil && rows == 0 {
		return fmt.Errorf("outbox delivery %q for consumer %q is not inflight", factID, consumer)
	}
	return nil
}

func normalizeOutboxConfig(config OutboxConfig) OutboxConfig {
	if config.FactTable == "" {
		config.FactTable = defaultFactTable
	}
	if config.DeliveryTable == "" {
		config.DeliveryTable = defaultDeliveryTable
	}
	if config.ClaimTimeout <= 0 {
		config.ClaimTimeout = defaultClaimTimeout
	}
	if config.MaxAttempts <= 0 {
		config.MaxAttempts = defaultMaxAttempts
	}
	return config
}

func (s *OutboxStore) validate() error {
	if s == nil || s.database == nil || s.database.DB() == nil {
		return fmt.Errorf("sql outbox requires a database")
	}
	if _, err := s.quote(s.config.FactTable); err != nil {
		return err
	}
	if _, err := s.quote(s.config.DeliveryTable); err != nil {
		return err
	}
	return nil
}

func (s *OutboxStore) validateConsumer(consumer string) error {
	if err := s.validate(); err != nil {
		return err
	}
	if strings.TrimSpace(consumer) == "" {
		return fmt.Errorf("outbox consumer is required")
	}
	return nil
}

func (s *OutboxStore) validateConsumerFact(consumer string, factID string) error {
	if err := s.validateConsumer(consumer); err != nil {
		return err
	}
	if factID == "" {
		return fmt.Errorf("outbox fact id is required")
	}
	return nil
}

func (s *OutboxStore) schemaSQL() ([]string, error) {
	factTable, err := s.quote(s.config.FactTable)
	if err != nil {
		return nil, err
	}
	deliveryTable, err := s.quote(s.config.DeliveryTable)
	if err != nil {
		return nil, err
	}
	idType := "TEXT"
	shortTextType := "TEXT"
	payloadType := "BLOB"
	if s.database.Dialect() == dbx.MySQL {
		idType = "VARCHAR(128)"
		shortTextType = "VARCHAR(255)"
		payloadType = "LONGBLOB"
	}
	if s.database.Dialect() == dbx.Postgres {
		payloadType = "BYTEA"
	}
	return []string{
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
%s %s PRIMARY KEY,
%s %s NOT NULL,
%s %s NOT NULL,
%s %s NOT NULL,
%s %s NOT NULL,
%s %s NOT NULL,
%s INTEGER NOT NULL,
%s %s NOT NULL,
%s %s NOT NULL,
%s %s NOT NULL
)`,
			factTable,
			s.q("id"), idType,
			s.q("type"), shortTextType,
			s.q("aggregate_id"), shortTextType,
			s.q("tenant_id"), shortTextType,
			s.q("actor_id"), shortTextType,
			s.q("trace_id"), shortTextType,
			s.q("schema_version"),
			s.q("occurred_at"), shortTextType,
			s.q("payload"), payloadType,
			s.q("created_at"), shortTextType,
		),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
%s %s NOT NULL,
%s %s NOT NULL,
%s %s NOT NULL,
%s INTEGER NOT NULL,
%s %s NOT NULL,
%s TEXT NOT NULL,
%s %s NOT NULL,
PRIMARY KEY (%s, %s)
)`,
			deliveryTable,
			s.q("consumer"), idType,
			s.q("fact_id"), idType,
			s.q("status"), shortTextType,
			s.q("attempts"),
			s.q("available_at"), shortTextType,
			s.q("last_error"),
			s.q("updated_at"), shortTextType,
			s.q("consumer"), s.q("fact_id"),
		),
	}, nil
}

func (s *OutboxStore) initIndexes(ctx context.Context) error {
	if err := ensureSQLIndex(ctx, s.database, s.quote, s.config.FactTable, s.config.FactTable+"_occurred_idx", []string{"occurred_at", "id"}); err != nil {
		return err
	}
	return ensureSQLIndex(ctx, s.database, s.quote, s.config.DeliveryTable, s.config.DeliveryTable+"_fetch_idx", []string{"consumer", "status", "available_at", "updated_at"})
}

func (s *OutboxStore) ensureDeliveries(ctx context.Context, consumer string) error {
	factTable, err := s.quote(s.config.FactTable)
	if err != nil {
		return err
	}
	deliveryTable, err := s.quote(s.config.DeliveryTable)
	if err != nil {
		return err
	}
	insertVerb := "INSERT INTO"
	conflictSuffix := ""
	switch s.database.Dialect() {
	case dbx.SQLite:
		insertVerb = "INSERT OR IGNORE INTO"
	case dbx.MySQL:
		insertVerb = "INSERT IGNORE INTO"
	case dbx.Postgres:
		conflictSuffix = fmt.Sprintf(" ON CONFLICT (%s, %s) DO NOTHING", s.q("consumer"), s.q("fact_id"))
	}
	query := fmt.Sprintf(`%s %s (%s, %s, %s, %s, %s, %s, %s)
SELECT %s, f.%s, %s, 0, %s, '', %s
FROM %s f
WHERE NOT EXISTS (
SELECT 1 FROM %s d WHERE d.%s = %s AND d.%s = f.%s
) %s`,
		insertVerb,
		deliveryTable,
		s.q("consumer"), s.q("fact_id"), s.q("status"), s.q("attempts"), s.q("available_at"), s.q("last_error"), s.q("updated_at"),
		s.placeholder(1), s.q("id"), s.placeholder(2), s.placeholder(3), s.placeholder(4),
		factTable,
		deliveryTable, s.q("consumer"), s.placeholder(5), s.q("fact_id"), s.q("id"),
		conflictSuffix,
	)
	now := outboxTime(time.Now())
	_, err = s.execer(ctx).ExecContext(ctx, query,
		consumer,
		string(outbox.StatusPending),
		now,
		now,
		consumer,
	)
	return err
}

func (s *OutboxStore) deliveryStatus(ctx context.Context, consumer string, factID string) (outbox.Status, bool, error) {
	status, _, err := s.deliveryState(ctx, consumer, factID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	return status, true, nil
}

func (s *OutboxStore) deliveryState(ctx context.Context, consumer string, factID string) (outbox.Status, int, error) {
	deliveryTable, err := s.quote(s.config.DeliveryTable)
	if err != nil {
		return "", 0, err
	}
	query := fmt.Sprintf("SELECT %s, %s FROM %s WHERE %s=%s AND %s=%s",
		s.q("status"),
		s.q("attempts"),
		deliveryTable,
		s.q("consumer"), s.placeholder(1),
		s.q("fact_id"), s.placeholder(2),
	)
	var status outbox.Status
	var attempts int
	if err := s.queryer(ctx).QueryRowContext(ctx, query, consumer, factID).Scan(&status, &attempts); err != nil {
		if err == sql.ErrNoRows {
			return "", 0, fmt.Errorf("outbox delivery not found for consumer %q fact %q: %w", consumer, factID, sql.ErrNoRows)
		}
		return "", 0, err
	}
	return status, attempts, nil
}

type sqlExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

type sqlQueryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (s *OutboxStore) execer(ctx context.Context) sqlExecer {
	if tx, ok := TxFromContext(ctx); ok && tx != nil {
		return tx
	}
	return s.database.DB()
}

func (s *OutboxStore) queryer(ctx context.Context) sqlQueryer {
	if tx, ok := TxFromContext(ctx); ok && tx != nil && tx.SQLTx() != nil {
		return tx.SQLTx()
	}
	return s.database.DB()
}

func (s *OutboxStore) q(identifier string) string {
	quoted, _ := s.quote(identifier)
	return quoted
}

func (s *OutboxStore) quoteList(columns []string) string {
	quoted := make([]string, 0, len(columns))
	for _, column := range columns {
		quoted = append(quoted, s.q(column))
	}
	return strings.Join(quoted, ", ")
}

func (s *OutboxStore) quote(identifier string) (string, error) {
	if !sqlIdentifierPattern.MatchString(identifier) {
		return "", fmt.Errorf("invalid sql identifier %q", identifier)
	}
	switch s.database.Dialect() {
	case dbx.MySQL, dbx.SQLite:
		return "`" + identifier + "`", nil
	default:
		return `"` + identifier + `"`, nil
	}
}

func (s *OutboxStore) placeholders(start int, count int) string {
	items := make([]string, 0, count)
	for i := 0; i < count; i++ {
		items = append(items, s.placeholder(start+i))
	}
	return strings.Join(items, ", ")
}

func (s *OutboxStore) placeholder(index int) string {
	if s.database.Dialect() == dbx.Postgres {
		return fmt.Sprintf("$%d", index)
	}
	return "?"
}

func outboxTime(t time.Time) string {
	return t.UTC().Format(outboxTimeLayout)
}

func parseOutboxTime(value string) time.Time {
	parsed, err := time.Parse(outboxTimeLayout, value)
	if err != nil {
		return time.Time{}
	}
	return parsed
}
