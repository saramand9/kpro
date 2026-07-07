// File: internal/modules/users/service/port.go
// Purpose: Defines users service persistence ports.

package service

import (
	"context"

	"github.com/saramand9/bignet/kpro/internal/modules/users"
)

// UserStore is the users module persistence port.
//
// Store adapters backed by database/dbx, database/gorm, or other databases can
// satisfy this interface, but the service does not depend on those frameworks.
type UserStore interface {
	List(ctx context.Context) ([]users.User, error)
	Find(ctx context.Context, id int64) (users.User, error)
	Create(ctx context.Context, user users.User) (users.User, error)
	Update(ctx context.Context, user users.User) (users.User, error)
	Delete(ctx context.Context, id int64) error
}
