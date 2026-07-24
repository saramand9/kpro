package gormstore

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/saramand9/kpro/database/common/lifecycle"
	"gorm.io/gorm"
)

const defaultPingTimeout = 5 * time.Second

// Config controls a GORM database resource.
type Config struct {
	// Name is the resource name used in health reports.
	Name string
	// Dialector opens a new GORM database when DB is nil.
	Dialector gorm.Dialector
	// GORMConfig is passed to gorm.Open when Dialector is used.
	GORMConfig *gorm.Config
	// DB reuses an already opened GORM database. Its database/sql pool remains
	// caller-owned and is never closed by Database.Close.
	DB              *gorm.DB
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
	PingTimeout     time.Duration
}

// Database is a named SQL resource backed by a native GORM handle.
type Database struct {
	name string
	db   *gorm.DB
	owns bool
}

var _ lifecycle.Resource = (*Database)(nil)

// Open creates a GORM-backed data resource.
func Open(ctx context.Context, config Config) (*Database, error) {
	if config.DB != nil && config.Dialector != nil {
		return nil, fmt.Errorf("%w: use DB or Dialector, not both", ErrInvalidConfig)
	}
	db := config.DB
	owns := db == nil
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
	database := wrap(config.Name, db, owns)
	sqlDB, err := database.SQLDB()
	if err != nil {
		_ = database.Close()
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

// Wrap exposes an existing caller-owned GORM database as a lifecycle resource.
func Wrap(name string, db *gorm.DB) *Database {
	return wrap(name, db, false)
}

func wrap(name string, db *gorm.DB, owns bool) *Database {
	if name == "" {
		name = "main"
	}
	return &Database{name: name, db: db, owns: owns}
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

// GORM returns the native GORM handle.
func (d *Database) GORM() *gorm.DB {
	if d == nil {
		return nil
	}
	return d.db
}

// SQLDB returns the database/sql pool used by GORM.
func (d *Database) SQLDB() (*sql.DB, error) {
	if d == nil || d.db == nil {
		return nil, fmt.Errorf("%w: gorm database is nil", lifecycle.ErrUnavailable)
	}
	return d.db.DB()
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
	return lifecycle.KindSQL
}

// Ping implements lifecycle.Resource.
func (d *Database) Ping(ctx context.Context) error {
	sqlDB, err := d.SQLDB()
	if err != nil {
		return err
	}
	return sqlDB.PingContext(ctx)
}

// Stats returns database/sql pool statistics.
func (d *Database) Stats() sql.DBStats {
	sqlDB, err := d.SQLDB()
	if err != nil {
		return sql.DBStats{}
	}
	return sqlDB.Stats()
}

// Health implements lifecycle.Resource.
func (d *Database) Health(ctx context.Context) lifecycle.Health {
	health := lifecycle.CheckHealth(ctx, d)
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

// Close implements lifecycle.Resource.
func (d *Database) Close() error {
	return d.Shutdown(context.Background())
}

// Shutdown closes the database/sql pool only when Open created it from a
// Dialector. Pools supplied through Config.DB or Wrap remain caller-owned.
func (d *Database) Shutdown(context.Context) error {
	if d == nil || !d.owns {
		return nil
	}
	sqlDB, err := d.SQLDB()
	if err != nil {
		return nil
	}
	return sqlDB.Close()
}
