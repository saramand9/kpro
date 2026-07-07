package gormstore

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/saramand9/bignet/kpro/database/kernel"
	"gorm.io/gorm"
)

const defaultPingTimeout = 5 * time.Second

// Config controls a GORM storage resource.
type Config struct {
	// Name is the resource name used in health reports.
	Name string
	// Dialector opens a new GORM database when DB is nil.
	Dialector gorm.Dialector
	// GORMConfig is passed to gorm.Open when Dialector is used.
	GORMConfig *gorm.Config
	// DB reuses an already opened GORM database.
	DB              *gorm.DB
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
	PingTimeout     time.Duration
}

// Database is a named SQL resource backed by GORM.
type Database struct {
	name string
	db   *gorm.DB
}

var _ kernel.Resource = (*Database)(nil)

// Open creates a GORM-backed data resource.
func Open(ctx context.Context, config Config) (*Database, error) {
	if config.DB != nil && config.Dialector != nil {
		return nil, fmt.Errorf("%w: use DB or Dialector, not both", ErrInvalidConfig)
	}
	db := config.DB
	if db == nil {
		if config.Dialector == nil {
			return nil, fmt.Errorf("%w: dialector or DB is required", ErrInvalidConfig)
		}
		var err error
		if config.GORMConfig != nil {
			db, err = gorm.Open(config.Dialector, config.GORMConfig)
		} else {
			db, err = gorm.Open(config.Dialector)
		}
		if err != nil {
			return nil, err
		}
	}
	database := Wrap(config.Name, db)
	sqlDB, err := database.SQLDB()
	if err != nil {
		return nil, err
	}
	applyPoolConfig(sqlDB, config)
	if config.PingTimeout <= 0 {
		config.PingTimeout = defaultPingTimeout
	}
	if ctx != nil {
		pingCtx, cancel := context.WithTimeout(ctx, config.PingTimeout)
		defer cancel()
		if err := database.Ping(pingCtx); err != nil {
			_ = database.Close()
			return nil, err
		}
	}
	return database, nil
}

// Wrap exposes an existing GORM database as a data resource.
func Wrap(name string, db *gorm.DB) *Database {
	if name == "" {
		name = "main"
	}
	return &Database{name: name, db: db}
}

func applyPoolConfig(db *sql.DB, config Config) {
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
}

// GORM returns the underlying GORM handle.
func (d *Database) GORM() *gorm.DB {
	if d == nil {
		return nil
	}
	return d.db
}

// SQLDB returns the database/sql pool used by GORM.
func (d *Database) SQLDB() (*sql.DB, error) {
	if d == nil || d.db == nil {
		return nil, fmt.Errorf("%w: gorm database is nil", kernel.ErrUnavailable)
	}
	return d.db.DB()
}

// Name implements kernel.Resource.
func (d *Database) Name() string {
	if d == nil {
		return ""
	}
	return d.name
}

// Kind implements kernel.Resource.
func (d *Database) Kind() kernel.Kind {
	return kernel.KindSQL
}

// Ping implements kernel.Resource.
func (d *Database) Ping(ctx context.Context) error {
	sqlDB, err := d.SQLDB()
	if err != nil {
		return err
	}
	return sqlDB.PingContext(ctx)
}

// Exec executes raw SQL through GORM.
func (d *Database) Exec(ctx context.Context, query string, args ...any) error {
	if d == nil || d.db == nil {
		return sql.ErrConnDone
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return d.db.WithContext(ctx).Exec(query, args...).Error
}

// Tx executes fn in a GORM transaction.
func (d *Database) Tx(ctx context.Context, fn func(*gorm.DB) error) error {
	if d == nil || d.db == nil {
		return sql.ErrConnDone
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return d.db.WithContext(ctx).Transaction(fn)
}

// Transaction executes fn in a GORM transaction.
func (d *Database) Transaction(ctx context.Context, fn func(*gorm.DB) error) error {
	return d.Tx(ctx, fn)
}

// Stats returns database/sql pool statistics.
func (d *Database) Stats() sql.DBStats {
	sqlDB, err := d.SQLDB()
	if err != nil {
		return sql.DBStats{}
	}
	return sqlDB.Stats()
}

// Health implements kernel.Resource.
func (d *Database) Health(ctx context.Context) kernel.Health {
	health := kernel.CheckHealth(ctx, d)
	sqlDB, err := d.SQLDB()
	if err == nil {
		stats := sqlDB.Stats()
		health.Details = map[string]any{
			"open_connections": stats.OpenConnections,
			"in_use":           stats.InUse,
			"idle":             stats.Idle,
		}
	}
	return health
}

// Close implements kernel.Resource.
func (d *Database) Close() error {
	sqlDB, err := d.SQLDB()
	if err != nil {
		return nil
	}
	return sqlDB.Close()
}

// Shutdown closes the underlying database/sql pool for server lifecycle hooks.
func (d *Database) Shutdown(context.Context) error {
	return d.Close()
}
