package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/saramand9/bignet/kpro/database/dbx"
)

func ensureSQLIndex(ctx context.Context, database *dbx.Database, quote func(string) (string, error), tableName, indexName string, columns []string) error {
	exists, err := sqlIndexExists(ctx, database, tableName, indexName)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	index, err := quote(indexName)
	if err != nil {
		return err
	}
	table, err := quote(tableName)
	if err != nil {
		return err
	}
	quotedColumns := make([]string, 0, len(columns))
	for _, column := range columns {
		quoted, err := quote(column)
		if err != nil {
			return err
		}
		quotedColumns = append(quotedColumns, quoted)
	}
	query := fmt.Sprintf("CREATE INDEX %s ON %s (%s)", index, table, strings.Join(quotedColumns, ", "))
	if _, err := database.DB().ExecContext(ctx, query); err != nil {
		exists, existsErr := sqlIndexExists(ctx, database, tableName, indexName)
		if existsErr == nil && exists {
			return nil
		}
		return err
	}
	return nil
}

func sqlIndexExists(ctx context.Context, database *dbx.Database, tableName, indexName string) (bool, error) {
	var query string
	var args []any
	switch database.Dialect() {
	case dbx.SQLite:
		query = "SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?"
		args = []any{indexName}
	case dbx.MySQL:
		query = "SELECT INDEX_NAME FROM INFORMATION_SCHEMA.STATISTICS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND INDEX_NAME = ? LIMIT 1"
		args = []any{tableName, indexName}
	default:
		query = "SELECT indexname FROM pg_indexes WHERE schemaname = current_schema() AND tablename = $1 AND indexname = $2"
		args = []any{tableName, indexName}
	}
	var found string
	err := database.DB().QueryRowContext(ctx, query, args...).Scan(&found)
	if err == nil {
		return true, nil
	}
	if err == sql.ErrNoRows {
		return false, nil
	}
	return false, err
}
