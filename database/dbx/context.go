package dbx

import "context"

type txContextKey struct{}

// WithTx stores a dbx transaction in context for Store and repository helpers.
func WithTx(ctx context.Context, tx *Tx) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, txContextKey{}, tx)
}

// TxFromContext returns the active dbx transaction, if one exists.
func TxFromContext(ctx context.Context) (*Tx, bool) {
	if ctx == nil {
		return nil, false
	}
	tx, ok := ctx.Value(txContextKey{}).(*Tx)
	return tx, ok && tx != nil
}
