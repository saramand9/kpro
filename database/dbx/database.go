// File: database/dbx/database.go
// Purpose: Wraps database/sql connection pools for KPro database modules.

package dbx

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Database owns a database/sql pool plus sync and async writers.
type Database struct {
	db           *sql.DB
	dialect      Dialect
	writeTimeout time.Duration
	writer       *Writer
	async        *AsyncWriter
}

// Open creates a Database, configures its connection pool, and verifies it with PingContext.
func Open(ctx context.Context, config Config) (*Database, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	config, err := normalizeConfig(config)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open(config.DriverName, config.DSN)
	if err != nil {
		return nil, err
	}
	if config.MaxOpenConns > 0 {
		db.SetMaxOpenConns(config.MaxOpenConns)
	}
	if config.MaxIdleConns > 0 {
		db.SetMaxIdleConns(config.MaxIdleConns)
	}
	if config.ConnMaxLifetime > 0 {
		db.SetConnMaxLifetime(config.ConnMaxLifetime)
	}
	if config.ConnMaxIdleTime > 0 {
		db.SetConnMaxIdleTime(config.ConnMaxIdleTime)
	}
	pingCtx, cancel := context.WithTimeout(ctx, config.PingTimeout)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, err
	}

	database := &Database{
		db:           db,
		dialect:      config.Dialect,
		writeTimeout: config.WriteTimeout,
	}
	database.writer = NewWriter(db, config.Dialect, WriterConfig{WriteTimeout: config.WriteTimeout})
	database.async = NewAsyncWriter(database.writer, config.Async)
	return database, nil
}

// DB returns the underlying database/sql pool for advanced use.
func (d *Database) DB() *sql.DB {
	if d == nil {
		return nil
	}
	return d.db
}

// Dialect returns the configured SQL dialect.
func (d *Database) Dialect() Dialect {
	if d == nil {
		return ""
	}
	return d.dialect
}

// Writer returns the synchronous writer.
func (d *Database) Writer() *Writer {
	if d == nil {
		return nil
	}
	return d.writer
}

// Async returns the bounded async writer.
func (d *Database) Async() *AsyncWriter {
	if d == nil {
		return nil
	}
	return d.async
}

// Stats returns database/sql pool statistics.
func (d *Database) Stats() sql.DBStats {
	if d == nil || d.db == nil {
		return sql.DBStats{}
	}
	return d.db.Stats()
}

// Close closes async workers and the underlying database/sql pool.
func (d *Database) Close() error {
	return d.Shutdown(context.Background())
}

// Shutdown drains async writes and closes the underlying database/sql pool.
func (d *Database) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if d == nil {
		return nil
	}
	var errs []error
	if d.async != nil {
		if err := d.async.Shutdown(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	if d.db != nil {
		if err := d.db.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Tx executes fn in a database transaction.
func (d *Database) Tx(ctx context.Context, fn func(*Tx) error, opts ...*sql.TxOptions) error {
	if d == nil || d.db == nil {
		return sql.ErrConnDone
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var txOpts *sql.TxOptions
	if len(opts) > 0 {
		txOpts = opts[0]
	}
	tx, err := d.db.BeginTx(ctx, txOpts)
	if err != nil {
		return err
	}
	wrapped := &Tx{
		tx:     tx,
		writer: NewWriter(tx, d.dialect, WriterConfig{WriteTimeout: d.writeTimeout}),
	}
	if err := fn(wrapped); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Tx wraps a database transaction with dbx helpers.
type Tx struct {
	tx     *sql.Tx
	writer *Writer
}

// SQLTx returns the underlying transaction for advanced use.
func (t *Tx) SQLTx() *sql.Tx {
	if t == nil {
		return nil
	}
	return t.tx
}

// Writer returns a writer bound to this transaction.
func (t *Tx) Writer() *Writer {
	if t == nil {
		return nil
	}
	return t.writer
}

// ExecContext delegates to the underlying transaction.
func (t *Tx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if t == nil || t.tx == nil {
		return nil, sql.ErrTxDone
	}
	return t.tx.ExecContext(ctx, query, args...)
}
