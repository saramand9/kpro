// File: internal/modules/projects/service/service.go
// Purpose: Provides project business operations over persistence ports.

package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/saramand9/bignet/kpro/internal/modules/projects"
)

const (
	defaultStatus = "active"
)

// ErrInvalidInput reports invalid project service input.
var ErrInvalidInput = errors.New("invalid project input")

// Service owns project business operations and hides storage details from handlers.
type Service struct {
	store ProjectStore
}

// NewWithStore creates a project service over an existing store.
func NewWithStore(store ProjectStore) *Service {
	return &Service{store: store}
}

// List returns all projects.
func (s *Service) List(ctx context.Context) ([]projects.Project, error) {
	return s.store.List(ctx)
}

// Find returns one project by id.
func (s *Service) Find(ctx context.Context, id int64) (projects.Project, error) {
	return s.store.Find(ctx, id)
}

// Create validates and stores a new project, returning the persisted row.
func (s *Service) Create(ctx context.Context, input CreateInput) (projects.Project, error) {
	input.normalize()
	if err := input.validate(); err != nil {
		return projects.Project{}, err
	}
	project := projects.Project{
		Name:        input.Name,
		Description: input.Description,
		Status:      input.Status,
	}
	return s.store.Create(ctx, project)
}

// Update applies a partial update and returns the persisted row.
func (s *Service) Update(ctx context.Context, id int64, input UpdateInput) (projects.Project, error) {
	current, err := s.Find(ctx, id)
	if err != nil {
		return projects.Project{}, err
	}
	updated, err := input.apply(current)
	if err != nil {
		return projects.Project{}, err
	}
	return s.store.Update(ctx, updated)
}

// Delete removes a project by id.
func (s *Service) Delete(ctx context.Context, id int64) error {
	return s.store.Delete(ctx, id)
}

func (i *CreateInput) normalize() {
	i.Name = strings.TrimSpace(i.Name)
	i.Description = strings.TrimSpace(i.Description)
	i.Status = normalizeStatus(i.Status)
}

func (i CreateInput) validate() error {
	if i.Name == "" {
		return fmt.Errorf("%w: name is required", ErrInvalidInput)
	}
	if !isValidStatus(i.Status) {
		return fmt.Errorf("%w: status must be active, paused, or archived", ErrInvalidInput)
	}
	return nil
}

func (i UpdateInput) apply(project projects.Project) (projects.Project, error) {
	if i.Name == nil && i.Description == nil && i.Status == nil {
		return project, fmt.Errorf("%w: name, description, or status is required", ErrInvalidInput)
	}
	if i.Name != nil {
		name := strings.TrimSpace(*i.Name)
		if name == "" {
			return project, fmt.Errorf("%w: name is required", ErrInvalidInput)
		}
		project.Name = name
	}
	if i.Description != nil {
		project.Description = strings.TrimSpace(*i.Description)
	}
	if i.Status != nil {
		status := normalizeStatus(*i.Status)
		if !isValidStatus(status) {
			return project, fmt.Errorf("%w: status must be active, paused, or archived", ErrInvalidInput)
		}
		project.Status = status
	}
	return project, nil
}

func normalizeStatus(status string) string {
	status = strings.ToLower(strings.TrimSpace(status))
	if status == "" {
		return defaultStatus
	}
	return status
}

func isValidStatus(status string) bool {
	switch status {
	case "active", "paused", "archived":
		return true
	default:
		return false
	}
}
