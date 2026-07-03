// File: internal/modules/projects/service/dto.go
// Purpose: Defines service-level input DTOs for project business operations.

package service

// CreateInput is the service input for creating a project.
type CreateInput struct {
	Name        string
	Description string
	Status      string
}

// UpdateInput is the service input for partially updating a project.
type UpdateInput struct {
	Name        *string
	Description *string
	Status      *string
}
