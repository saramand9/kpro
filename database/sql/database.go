// Package sqlstore adapts the existing dbx SQL implementation into the database layer.
package sqlstore

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/saramand9/bignet/kpro/database/dbx"
	"github.com/saramand9/bignet/kpro/database/kernel"
)

// Database is a named SQL resource backed by dbx.Database.
type Database struct {
	name     string
	database *dbx.Database
}

var _ kernel.Resource = (*Database)(nil)

// Wrap exposes an existing dbx database as a data resource.
func Wrap(name string, database *dbx.Database) *Database {
	if name == "" {
		name = "main"
	}
	return &Database{name: name, database: database}
}

// Open creates and wraps a dbx database.
func Open(ctx context.Context, name string, config dbx.Config) (*Database, error) {
	database, err := dbx.Open(ctx, config)
	if err != nil {
		return nil, err
	}
	return Wrap(name, database), nil
}

// DBX returns the underlying dbx database for existing repositories.
func (d *Database) DBX() *dbx.Database {
	if d == nil {
		return nil
	}
	return d.database
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
	if d == nil || d.database == nil || d.database.DB() == nil {
		return fmt.Errorf("%w: sql database is nil", kernel.ErrUnavailable)
	}
	return d.database.DB().PingContext(ctx)
}

// Health implements kernel.Resource.
func (d *Database) Health(ctx context.Context) kernel.Health {
	health := kernel.CheckHealth(ctx, d)
	if d != nil && d.database != nil && d.database.DB() != nil {
		stats := d.database.Stats()
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
	if d == nil || d.database == nil {
		return nil
	}
	return d.database.Close()
}

// Stats returns database/sql pool stats.
func (d *Database) Stats() sql.DBStats {
	if d == nil || d.database == nil {
		return sql.DBStats{}
	}
	return d.database.Stats()
}
