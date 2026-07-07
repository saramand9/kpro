// File: internal/modules/projects/service/port.go
// Purpose: Defines projects service persistence ports.

package service

import (
	"context"

	"github.com/saramand9/bignet/kpro/internal/modules/projects"
)

// ProjectStore is the projects module persistence port.
//
// Store adapters backed by database/dbx, database/gorm, or other databases can
// satisfy this interface, but the service does not depend on those frameworks.
type ProjectStore interface {
	List(ctx context.Context) ([]projects.Project, error)
	Find(ctx context.Context, id int64) (projects.Project, error)
	Create(ctx context.Context, project projects.Project) (projects.Project, error)
	Update(ctx context.Context, project projects.Project) (projects.Project, error)
	Delete(ctx context.Context, id int64) error
}
