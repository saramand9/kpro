// File: internal/app/persistence/resources.go
// Purpose: Owns application database resources and their lifecycle.

package persistence

import (
	"context"
	"strings"

	"github.com/saramand9/bignet/kpro/database/common/lifecycle"
	gormstore "github.com/saramand9/bignet/kpro/database/gorm"
	"gorm.io/driver/sqlite"

	_ "modernc.org/sqlite"
)

// Config declares database resources used by the app process.
type Config struct {
	// GORMDSN is the sqlite DSN for SQL-backed app modules. Empty disables SQL.
	GORMDSN string
}

// Resources owns opened database resources for health checks and shutdown.
type Resources struct {
	SQL *gormstore.Database
}

// Open initializes configured database resources.
func Open(ctx context.Context, config Config) (*Resources, error) {
	resources := &Resources{}
	if strings.TrimSpace(config.GORMDSN) == "" {
		return resources, nil
	}
	database, err := gormstore.Open(ctx, gormstore.Config{
		Name:         "app",
		Dialector:    sqlite.Dialector{DriverName: "sqlite", DSN: config.GORMDSN},
		MaxOpenConns: 1,
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
