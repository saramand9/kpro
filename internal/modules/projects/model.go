// File: internal/modules/projects/model.go
// Purpose: Defines the project domain model.

package projects

import (
	"time"
)

// TableName is the projects source-of-truth table.
const TableName = "projects"

// Project is the projects module domain entity.
type Project struct {
	ID          int64
	Name        string
	Description string
	Status      string
	CreatedAt   time.Time
}
