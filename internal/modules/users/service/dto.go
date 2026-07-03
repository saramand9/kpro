// File: internal/modules/users/service/dto.go
// Purpose: Defines service-level input DTOs for user business operations.

package service

// CreateInput is the service input for creating a user.
type CreateInput struct {
	Name string
	Age  int
}

// UpdateInput is the service input for partially updating a user.
type UpdateInput struct {
	Name *string
	Age  *int
}
