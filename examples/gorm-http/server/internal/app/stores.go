// File: examples/gorm-http/server/internal/app/stores.go
// Purpose: Centralizes application store composition for database examples.

package app

import (
	"fmt"

	gormstore "github.com/saramand9/bignet/kpro/database/gorm"
	projectservice "github.com/saramand9/bignet/kpro/internal/modules/projects/service"
	projectstore "github.com/saramand9/bignet/kpro/internal/modules/projects/store"
	userservice "github.com/saramand9/bignet/kpro/internal/modules/users/service"
	userstore "github.com/saramand9/bignet/kpro/internal/modules/users/store"
)

// Stores contains business persistence ports, not concrete database frameworks.
type Stores struct {
	Users    userservice.UserStore
	Projects projectservice.ProjectStore
}

// NewGORMStores wires all module stores to the GORM implementation.
func NewGORMStores(database *gormstore.Database) Stores {
	return Stores{
		Users:    userstore.NewGORM(database),
		Projects: projectstore.NewGORM(database),
	}
}

func (s Stores) validate() error {
	if s.Users == nil {
		return fmt.Errorf("users store is required")
	}
	if s.Projects == nil {
		return fmt.Errorf("projects store is required")
	}
	return nil
}
