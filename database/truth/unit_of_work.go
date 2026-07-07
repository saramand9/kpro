// Package truth defines strong-consistency boundaries.
package truth

import "context"

// UnitOfWork executes a callback inside one consistency boundary.
type UnitOfWork interface {
	Do(ctx context.Context, fn func(ctx context.Context) error) error
}

// Repository is a marker for source-of-truth persistence ports.
//
// Domain modules should define richer repository interfaces next to their
// aggregates. This marker exists only for architecture documentation and
// generic wiring.
type Repository interface {
	RepositoryName() string
}
