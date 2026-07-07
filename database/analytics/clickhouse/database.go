package clickhousestore

import (
	"context"
	"database/sql"

	_ "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/saramand9/bignet/kpro/database/contracts/lifecycle"
)

// Database owns a ClickHouse database/sql pool.
type Database struct {
	name string
	db   *sql.DB
	owns bool
}

var _ lifecycle.Resource = (*Database)(nil)

// Open creates a ClickHouse adapter and verifies it with Ping unless skipped.
func Open(ctx context.Context, config Config) (*Database, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	config, err := normalizeConfig(config)
	if err != nil {
		return nil, err
	}

	db := config.DB
	owns := false
	if db == nil {
		db, err = sql.Open(config.DriverName, config.DSN)
		if err != nil {
			return nil, err
		}
		owns = true
	}
	applyPoolConfig(db, config)

	database := &Database{name: config.Name, db: db, owns: owns}
	if !config.SkipPingCheck {
		pingCtx, cancel := context.WithTimeout(ctx, config.PingTimeout)
		defer cancel()
		if err := database.Ping(pingCtx); err != nil {
			if owns {
				_ = db.Close()
			}
			return nil, err
		}
	}
	return database, nil
}

// DB returns the underlying database/sql pool.
func (d *Database) DB() *sql.DB {
	if d == nil {
		return nil
	}
	return d.db
}

// Name implements lifecycle.Resource.
func (d *Database) Name() string {
	if d == nil {
		return ""
	}
	return d.name
}

// Kind implements lifecycle.Resource.
func (d *Database) Kind() lifecycle.Kind {
	return lifecycle.KindAnalytics
}

// Ping implements lifecycle.Resource.
func (d *Database) Ping(ctx context.Context) error {
	if d == nil || d.db == nil {
		return sql.ErrConnDone
	}
	return d.db.PingContext(contextOrBackground(ctx))
}

// Health implements lifecycle.Resource.
func (d *Database) Health(ctx context.Context) lifecycle.Health {
	health := lifecycle.CheckHealth(ctx, d)
	if d != nil && d.db != nil {
		stats := d.db.Stats()
		health.Details = map[string]any{
			"open_connections": stats.OpenConnections,
			"in_use":           stats.InUse,
			"idle":             stats.Idle,
			"max_open":         stats.MaxOpenConnections,
		}
	}
	return health
}

// Exec executes a ClickHouse SQL statement.
func (d *Database) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if d == nil || d.db == nil {
		return nil, sql.ErrConnDone
	}
	return d.db.ExecContext(contextOrBackground(ctx), query, args...)
}

// Query executes a ClickHouse SQL query.
func (d *Database) Query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if d == nil || d.db == nil {
		return nil, sql.ErrConnDone
	}
	return d.db.QueryContext(contextOrBackground(ctx), query, args...)
}

// Tx executes fn in a database/sql transaction.
func (d *Database) Tx(ctx context.Context, fn func(*sql.Tx) error, opts ...*sql.TxOptions) error {
	if d == nil || d.db == nil {
		return sql.ErrConnDone
	}
	var txOptions *sql.TxOptions
	if len(opts) > 0 {
		txOptions = opts[0]
	}
	tx, err := d.db.BeginTx(contextOrBackground(ctx), txOptions)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Stats returns database/sql pool statistics.
func (d *Database) Stats() sql.DBStats {
	if d == nil || d.db == nil {
		return sql.DBStats{}
	}
	return d.db.Stats()
}

// Close implements lifecycle.Resource.
func (d *Database) Close() error {
	return d.Shutdown(context.Background())
}

// Shutdown releases the database/sql pool when this adapter opened it.
func (d *Database) Shutdown(ctx context.Context) error {
	if d == nil || d.db == nil || !d.owns {
		return nil
	}
	return d.db.Close()
}

func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
