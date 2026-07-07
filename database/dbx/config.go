// File: database/dbx/config.go
// Purpose: Defines database connection and writer configuration.

package dbx

import "time"

// Dialect identifies SQL syntax differences needed by dbx.
type Dialect string

const (
	// SQLite generates SQLite-compatible SQL.
	SQLite Dialect = "sqlite"
	// MySQL generates MySQL-compatible SQL.
	MySQL Dialect = "mysql"
	// Postgres generates PostgreSQL-compatible SQL.
	Postgres Dialect = "postgres"
)

const (
	defaultPingTimeout  = 5 * time.Second
	defaultWriteTimeout = 5 * time.Second
	defaultAsyncWorkers = 4
	defaultAsyncQueue   = 1024
)

// Config controls database connection pooling and writers.
type Config struct {
	// DriverName is the database/sql driver name, such as sqlite, mysql or postgres.
	DriverName string
	// DSN is the driver-specific data source name.
	DSN string
	// Dialect controls generated SQL syntax. Empty values are inferred from DriverName.
	Dialect Dialect
	// MaxOpenConns limits concurrently open database connections.
	MaxOpenConns int
	// MaxIdleConns limits idle pooled database connections.
	MaxIdleConns int
	// ConnMaxLifetime limits how long a connection may be reused.
	ConnMaxLifetime time.Duration
	// ConnMaxIdleTime limits how long a connection may stay idle.
	ConnMaxIdleTime time.Duration
	// PingTimeout bounds Open's initial PingContext. Values <= 0 use a default.
	PingTimeout time.Duration
	// WriteTimeout bounds Writer operations. Values <= 0 use a default.
	WriteTimeout time.Duration
	// Async controls the optional async writer created with the Database.
	Async AsyncConfig
}

// AsyncConfig controls async database write workers.
type AsyncConfig struct {
	// Workers is the number of worker goroutines. Values <= 0 use a default.
	Workers int
	// QueueSize is the bounded pending job capacity. Values <= 0 use a default.
	QueueSize int
}

func normalizeConfig(config Config) (Config, error) {
	if config.DriverName == "" {
		return config, errMissingDriverName()
	}
	if config.Dialect == "" {
		config.Dialect = inferConfigDialect(config)
	}
	if err := config.Dialect.validate(); err != nil {
		return config, err
	}
	if config.PingTimeout <= 0 {
		config.PingTimeout = defaultPingTimeout
	}
	if config.WriteTimeout <= 0 {
		config.WriteTimeout = defaultWriteTimeout
	}
	if config.Async.Workers <= 0 {
		config.Async.Workers = defaultAsyncWorkers
	}
	if config.Async.QueueSize <= 0 {
		config.Async.QueueSize = defaultAsyncQueue
	}
	return config, nil
}

func inferConfigDialect(config Config) Dialect {
	if config.DriverName != "" {
		return inferDialect(config.DriverName)
	}
	return ""
}
