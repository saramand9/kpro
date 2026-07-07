package clickhousestore

import (
	"database/sql"
	"fmt"
	"time"
)

const (
	defaultDriverName  = "clickhouse"
	defaultPingTimeout = 5 * time.Second
)

// Config controls ClickHouse database/sql adapter setup.
type Config struct {
	Name string

	DriverName string
	DSN        string
	DB         *sql.DB

	SkipPingCheck bool
	PingTimeout   time.Duration

	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
}

func normalizeConfig(config Config) (Config, error) {
	if config.Name == "" {
		config.Name = "clickhouse"
	}
	if config.DriverName == "" {
		config.DriverName = defaultDriverName
	}
	if config.PingTimeout <= 0 {
		config.PingTimeout = defaultPingTimeout
	}
	if config.DB != nil && config.DSN != "" {
		return config, fmt.Errorf("%w: use DB or DSN, not both", ErrInvalidConfig)
	}
	if config.DB == nil && config.DSN == "" {
		return config, fmt.Errorf("%w: dsn or DB is required", ErrInvalidConfig)
	}
	return config, nil
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
