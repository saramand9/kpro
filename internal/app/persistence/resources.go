// File: internal/app/persistence/resources.go
// Purpose: Owns application database resources and their lifecycle.

package persistence

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/saramand9/kpro/database/common/lifecycle"
	gormstore "github.com/saramand9/kpro/database/gorm"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	_ "modernc.org/sqlite"
)

// SQLConfig declares the primary SQL resource used by app modules.
type SQLConfig struct {
	Driver          string
	DSN             string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
}

// Config declares database resources used by the app process.
type Config struct {
	SQL SQLConfig
}

// Resources owns opened database resources for health checks and shutdown.
type Resources struct {
	SQL *gormstore.Database
}

// Open initializes configured database resources.
func Open(ctx context.Context, config Config) (*Resources, error) {
	resources := &Resources{}
	if strings.TrimSpace(config.SQL.DSN) == "" {
		return resources, nil
	}
	dialector, err := sqlDialector(config.SQL)
	if err != nil {
		return nil, err
	}
	database, err := gormstore.Open(ctx, gormstore.Config{
		Name:            "app",
		Dialector:       dialector,
		MaxOpenConns:    config.SQL.MaxOpenConns,
		MaxIdleConns:    config.SQL.MaxIdleConns,
		ConnMaxLifetime: config.SQL.ConnMaxLifetime,
		ConnMaxIdleTime: config.SQL.ConnMaxIdleTime,
	})
	if err != nil {
		return nil, err
	}
	resources.SQL = database
	return resources, nil
}

// Managed reports whether this container owns at least one external resource.
func (r *Resources) Managed() bool {
	return r != nil && r.SQL != nil
}

// Health reports every opened data resource.
func (r *Resources) Health(ctx context.Context) []lifecycle.Health {
	return lifecycle.NewResourceSet(r.resources()...).Health(ctx)
}

// Close releases all opened data resources.
func (r *Resources) Close() error {
	return lifecycle.NewResourceSet(r.resources()...).Close()
}

// Shutdown releases resources during server shutdown.
func (r *Resources) Shutdown(context.Context) error {
	return r.Close()
}

func (r *Resources) resources() []lifecycle.Resource {
	if r == nil {
		return nil
	}
	resources := make([]lifecycle.Resource, 0, 1)
	if r.SQL != nil {
		resources = append(resources, r.SQL)
	}
	return resources
}

func sqlDialector(config SQLConfig) (gorm.Dialector, error) {
	driver := strings.ToLower(strings.TrimSpace(config.Driver))
	if driver == "" {
		driver = "sqlite"
	}
	switch driver {
	case "sqlite", "sqlite3":
		return sqlite.Dialector{DriverName: "sqlite", DSN: config.DSN}, nil
	case "mysql":
		return mysql.Open(config.DSN), nil
	case "postgres", "postgresql":
		return postgres.Open(config.DSN), nil
	default:
		return nil, fmt.Errorf("%w: unsupported gorm driver %q", gormstore.ErrInvalidConfig, config.Driver)
	}
}
