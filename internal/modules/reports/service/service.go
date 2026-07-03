// File: internal/modules/reports/service/service.go
// Purpose: Provides report business operations for protocol handlers.

package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrProjectRequired reports that a report request did not include a project.
var ErrProjectRequired = errors.New("project is required")

// Config controls the report service defaults.
type Config struct {
	DefaultOwner string
}

// Service owns report business operations.
type Service struct {
	defaultOwner string
}

// New creates a report service.
func New(config Config) *Service {
	if strings.TrimSpace(config.DefaultOwner) == "" {
		config.DefaultOwner = "system"
	}
	return &Service{defaultOwner: strings.TrimSpace(config.DefaultOwner)}
}

// BuildSummary validates input and returns a deterministic example report.
func (s *Service) BuildSummary(ctx context.Context, input BuildSummaryInput) (Summary, error) {
	select {
	case <-ctx.Done():
		return Summary{}, ctx.Err()
	default:
	}

	project := strings.TrimSpace(input.Project)
	if project == "" {
		return Summary{}, ErrProjectRequired
	}
	owner := strings.TrimSpace(input.Owner)
	if owner == "" {
		owner = s.defaultOwner
	}
	return Summary{
		Project: project,
		Owner:   owner,
		Title:   fmt.Sprintf("%s summary", project),
		Status:  "ready",
		Items:   int64(len(project)%5 + 1),
	}, nil
}
