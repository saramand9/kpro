package sqlstore

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/saramand9/bignet/kpro/database/dbx"
	"github.com/saramand9/bignet/kpro/database/truth"
)

// UnitOfWork executes callbacks inside a dbx SQL transaction.
type UnitOfWork struct {
	database *dbx.Database
	options  *sql.TxOptions
}

var _ truth.UnitOfWork = (*UnitOfWork)(nil)

// NewUnitOfWork creates a SQL unit of work over dbx.Database.
func NewUnitOfWork(database *dbx.Database, options ...*sql.TxOptions) *UnitOfWork {
	var txOptions *sql.TxOptions
	if len(options) > 0 {
		txOptions = options[0]
	}
	return &UnitOfWork{database: database, options: txOptions}
}

// Do implements truth.UnitOfWork.
func (u *UnitOfWork) Do(ctx context.Context, fn func(ctx context.Context) error) error {
	if u == nil || u.database == nil {
		return fmt.Errorf("sql unit of work requires a database")
	}
	return u.database.Tx(ctx, func(tx *dbx.Tx) error {
		return fn(WithTx(ctx, tx))
	}, u.options)
}

// WithTx stores a dbx transaction in context for repositories that need it.
func WithTx(ctx context.Context, tx *dbx.Tx) context.Context {
	return dbx.WithTx(ctx, tx)
}

// TxFromContext returns the active dbx transaction, if one exists.
func TxFromContext(ctx context.Context) (*dbx.Tx, bool) {
	return dbx.TxFromContext(ctx)
}
