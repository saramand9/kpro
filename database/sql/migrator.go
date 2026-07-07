package sqlstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/saramand9/bignet/kpro/database/dbx"
)

const defaultMigrationTable = "schema_migrations"

const (
	migrationStatusRunning = "running"
	migrationStatusApplied = "applied"
	migrationStatusFailed  = "failed"
)

// Migration is one ordered schema change.
type Migration struct {
	ID          string
	Description string
	Statements  []string
}

type migrationState struct {
	id        string
	checksum  string
	status    string
	startedAt string
	appliedAt string
	errText   string
}

// Migrator applies SQL migrations once and records applied IDs.
type Migrator struct {
	database *dbx.Database
	table    string
}

// NewMigrator creates a migrator for database. Empty table uses schema_migrations.
func NewMigrator(database *dbx.Database, table string) *Migrator {
	if table == "" {
		table = defaultMigrationTable
	}
	return &Migrator{database: database, table: table}
}

// Apply runs migrations in the order supplied.
func (m *Migrator) Apply(ctx context.Context, migrations ...Migration) error {
	if err := m.validate(); err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := m.init(ctx); err != nil {
		return err
	}
	for _, migration := range migrations {
		migration.ID = strings.TrimSpace(migration.ID)
		if migration.ID == "" {
			return fmt.Errorf("migration id is required")
		}
		if err := m.applyOne(ctx, migration); err != nil {
			return err
		}
	}
	return nil
}

// Applied returns applied migration IDs ordered by applied timestamp.
func (m *Migrator) Applied(ctx context.Context) ([]string, error) {
	if err := m.validate(); err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := m.init(ctx); err != nil {
		return nil, err
	}
	table, err := m.quote(m.table)
	if err != nil {
		return nil, err
	}
	query := fmt.Sprintf("SELECT %s FROM %s WHERE %s=%s ORDER BY %s, %s",
		m.q("id"),
		table,
		m.q("status"), m.placeholder(1),
		m.q("applied_at"), m.q("id"),
	)
	rows, err := m.database.DB().QueryContext(ctx, query, migrationStatusApplied)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (m *Migrator) init(ctx context.Context) error {
	table, err := m.quote(m.table)
	if err != nil {
		return err
	}
	idType := "TEXT"
	checksumType := "TEXT"
	statusType := "TEXT"
	timeType := "TEXT"
	descriptionType := "TEXT"
	errorType := "TEXT"
	if m.database.Dialect() == dbx.MySQL {
		idType = "VARCHAR(255)"
		checksumType = "VARCHAR(64)"
		statusType = "VARCHAR(32)"
		timeType = "VARCHAR(64)"
		descriptionType = "VARCHAR(1024)"
		errorType = "VARCHAR(4096)"
	}
	createTable := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
%s %s PRIMARY KEY,
%s %s NOT NULL,
%s %s NOT NULL,
%s %s NOT NULL,
%s %s NOT NULL,
%s %s NOT NULL,
%s %s NOT NULL
)`,
		table,
		m.q("id"), idType,
		m.q("checksum"), checksumType,
		m.q("status"), statusType,
		m.q("started_at"), timeType,
		m.q("applied_at"), timeType,
		m.q("description"), descriptionType,
		m.q("error"), errorType,
	)
	if _, err := m.database.DB().ExecContext(ctx, createTable); err != nil {
		return err
	}
	if err := m.ensureMetadataColumns(ctx, map[string]string{
		"checksum":    checksumType + " NOT NULL DEFAULT ''",
		"status":      statusType + " NOT NULL DEFAULT '" + migrationStatusApplied + "'",
		"started_at":  timeType + " NOT NULL DEFAULT ''",
		"applied_at":  timeType + " NOT NULL DEFAULT ''",
		"description": descriptionType + " NOT NULL DEFAULT ''",
		"error":       errorType + " NOT NULL DEFAULT ''",
	}); err != nil {
		return err
	}
	indexName := m.table + "_status_idx"
	if _, err := m.quote(indexName); err != nil {
		return err
	}
	return ensureSQLIndex(ctx, m.database, m.quote, m.table, indexName, []string{"status", "applied_at"})
}

func (m *Migrator) ensureMetadataColumns(ctx context.Context, definitions map[string]string) error {
	columns, err := m.metadataColumns(ctx)
	if err != nil {
		return err
	}
	table, err := m.quote(m.table)
	if err != nil {
		return err
	}
	for column, definition := range definitions {
		if columns[column] {
			continue
		}
		quoted, err := m.quote(column)
		if err != nil {
			return err
		}
		if _, err := m.database.DB().ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, quoted, definition)); err != nil {
			return err
		}
	}
	return nil
}

func (m *Migrator) metadataColumns(ctx context.Context) (map[string]bool, error) {
	columns := make(map[string]bool)
	var rows *sql.Rows
	var err error
	switch m.database.Dialect() {
	case dbx.SQLite:
		table, quoteErr := m.quote(m.table)
		if quoteErr != nil {
			return nil, quoteErr
		}
		rows, err = m.database.DB().QueryContext(ctx, "PRAGMA table_info("+table+")")
	case dbx.MySQL:
		rows, err = m.database.DB().QueryContext(ctx, `SELECT COLUMN_NAME FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?`, m.table)
	default:
		rows, err = m.database.DB().QueryContext(ctx, `SELECT column_name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = $1`, m.table)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if m.database.Dialect() == dbx.SQLite {
			var cid int
			var typ string
			var notNull int
			var defaultValue sql.NullString
			var pk int
			if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
				return nil, err
			}
		} else if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	return columns, rows.Err()
}

func (m *Migrator) state(ctx context.Context, id string) (migrationState, bool, error) {
	table, err := m.quote(m.table)
	if err != nil {
		return migrationState{}, false, err
	}
	query := fmt.Sprintf("SELECT %s, %s, %s, %s, %s, %s FROM %s WHERE %s=%s",
		m.q("id"), m.q("checksum"), m.q("status"), m.q("started_at"), m.q("applied_at"), m.q("error"),
		table,
		m.q("id"), m.placeholder(1),
	)
	var state migrationState
	err = m.database.DB().QueryRowContext(ctx, query, id).Scan(
		&state.id,
		&state.checksum,
		&state.status,
		&state.startedAt,
		&state.appliedAt,
		&state.errText,
	)
	if err == nil {
		return state, true, nil
	}
	if err == sql.ErrNoRows {
		return migrationState{}, false, nil
	}
	return migrationState{}, false, err
}

func (m *Migrator) applyOne(ctx context.Context, migration Migration) error {
	statements := nonEmptyStatements(migration.Statements)
	if len(statements) == 0 {
		return fmt.Errorf("migration %q has no statements", migration.ID)
	}
	checksum := migrationChecksum(statements)
	state, exists, err := m.state(ctx, migration.ID)
	if err != nil {
		return err
	}
	if exists {
		if state.status == migrationStatusApplied && state.checksum == "" {
			if err := m.backfillChecksum(ctx, migration.ID, checksum); err != nil {
				return err
			}
			state.checksum = checksum
		}
		return m.handleExistingMigration(state, checksum)
	}
	if err := m.insertRunning(ctx, migration, checksum); err != nil {
		state, exists, stateErr := m.state(ctx, migration.ID)
		if stateErr != nil {
			return err
		}
		if exists {
			return m.handleExistingMigration(state, checksum)
		}
		return err
	}
	if err := m.executeStatements(ctx, migration.ID, statements); err != nil {
		_ = m.markFailed(ctx, migration.ID, err)
		return err
	}
	return m.markApplied(ctx, migration.ID)
}

func (m *Migrator) handleExistingMigration(state migrationState, checksum string) error {
	if state.checksum != checksum {
		return fmt.Errorf("migration %q checksum mismatch", state.id)
	}
	switch state.status {
	case migrationStatusApplied:
		return nil
	case migrationStatusRunning:
		return fmt.Errorf("migration %q is already running", state.id)
	case migrationStatusFailed:
		if state.errText != "" {
			return fmt.Errorf("migration %q previously failed: %s", state.id, state.errText)
		}
		return fmt.Errorf("migration %q previously failed", state.id)
	default:
		return fmt.Errorf("migration %q has unknown status %q", state.id, state.status)
	}
}

func (m *Migrator) insertRunning(ctx context.Context, migration Migration, checksum string) error {
	table, err := m.quote(m.table)
	if err != nil {
		return err
	}
	now := migrationTime()
	query := fmt.Sprintf("INSERT INTO %s (%s, %s, %s, %s, %s, %s, %s) VALUES (%s, %s, %s, %s, %s, %s, %s)",
		table,
		m.q("id"), m.q("checksum"), m.q("status"), m.q("started_at"), m.q("applied_at"), m.q("description"), m.q("error"),
		m.placeholder(1), m.placeholder(2), m.placeholder(3), m.placeholder(4), m.placeholder(5), m.placeholder(6), m.placeholder(7),
	)
	_, err = m.database.DB().ExecContext(ctx, query,
		migration.ID,
		checksum,
		migrationStatusRunning,
		now,
		"",
		strings.TrimSpace(migration.Description),
		"",
	)
	return err
}

func (m *Migrator) executeStatements(ctx context.Context, id string, statements []string) error {
	return m.database.Tx(ctx, func(tx *dbx.Tx) error {
		for _, statement := range statements {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("migration %q: %w", id, err)
			}
		}
		return nil
	})
}

func (m *Migrator) markApplied(ctx context.Context, id string) error {
	table, err := m.quote(m.table)
	if err != nil {
		return err
	}
	query := fmt.Sprintf("UPDATE %s SET %s=%s, %s=%s, %s=%s WHERE %s=%s AND %s=%s",
		table,
		m.q("status"), m.placeholder(1),
		m.q("applied_at"), m.placeholder(2),
		m.q("error"), m.placeholder(3),
		m.q("id"), m.placeholder(4),
		m.q("status"), m.placeholder(5),
	)
	result, err := m.database.DB().ExecContext(ctx, query,
		migrationStatusApplied,
		migrationTime(),
		"",
		id,
		migrationStatusRunning,
	)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err == nil && rows == 0 {
		return fmt.Errorf("migration %q was not running", id)
	}
	return nil
}

func (m *Migrator) backfillChecksum(ctx context.Context, id string, checksum string) error {
	table, err := m.quote(m.table)
	if err != nil {
		return err
	}
	query := fmt.Sprintf("UPDATE %s SET %s=%s WHERE %s=%s AND %s=%s AND %s=%s",
		table,
		m.q("checksum"), m.placeholder(1),
		m.q("id"), m.placeholder(2),
		m.q("status"), m.placeholder(3),
		m.q("checksum"), m.placeholder(4),
	)
	_, err = m.database.DB().ExecContext(ctx, query,
		checksum,
		id,
		migrationStatusApplied,
		"",
	)
	return err
}

func (m *Migrator) markFailed(ctx context.Context, id string, migrationErr error) error {
	table, err := m.quote(m.table)
	if err != nil {
		return err
	}
	message := ""
	if migrationErr != nil {
		message = migrationErr.Error()
	}
	query := fmt.Sprintf("UPDATE %s SET %s=%s, %s=%s WHERE %s=%s AND %s=%s",
		table,
		m.q("status"), m.placeholder(1),
		m.q("error"), m.placeholder(2),
		m.q("id"), m.placeholder(3),
		m.q("status"), m.placeholder(4),
	)
	_, err = m.database.DB().ExecContext(ctx, query,
		migrationStatusFailed,
		message,
		id,
		migrationStatusRunning,
	)
	return err
}

func (m *Migrator) validate() error {
	if m == nil || m.database == nil || m.database.DB() == nil {
		return fmt.Errorf("sql migrator requires a database")
	}
	_, err := m.quote(m.table)
	return err
}

func (m *Migrator) q(identifier string) string {
	quoted, _ := m.quote(identifier)
	return quoted
}

func (m *Migrator) quote(identifier string) (string, error) {
	if !sqlIdentifierPattern.MatchString(identifier) {
		return "", fmt.Errorf("invalid sql identifier %q", identifier)
	}
	switch m.database.Dialect() {
	case dbx.MySQL, dbx.SQLite:
		return "`" + identifier + "`", nil
	default:
		return `"` + identifier + `"`, nil
	}
}

func (m *Migrator) placeholder(index int) string {
	if m.database.Dialect() == dbx.Postgres {
		return fmt.Sprintf("$%d", index)
	}
	return "?"
}

func nonEmptyStatements(statements []string) []string {
	nonEmpty := make([]string, 0, len(statements))
	for _, statement := range statements {
		statement = strings.TrimSpace(statement)
		if statement == "" {
			continue
		}
		nonEmpty = append(nonEmpty, statement)
	}
	return nonEmpty
}

func migrationChecksum(statements []string) string {
	sum := sha256.Sum256([]byte(strings.Join(statements, "\n;\n")))
	return hex.EncodeToString(sum[:])
}

func migrationTime() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}
