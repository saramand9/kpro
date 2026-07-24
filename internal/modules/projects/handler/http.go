// File: internal/modules/projects/handler/http.go
// Purpose: Provides explicit HTTP DTO handlers for the projects module.

package handler

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/saramand9/kpro/internal/modules/projects"
	projectservice "github.com/saramand9/kpro/internal/modules/projects/service"
	"github.com/saramand9/kpro/transport/httpx"
)

// CreateRequest is the public HTTP input for creating a project.
type CreateRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Status      string `json:"status"`
}

func (r CreateRequest) toInput() projectservice.CreateInput {
	return projectservice.CreateInput{
		Name:        r.Name,
		Description: r.Description,
		Status:      r.Status,
	}
}

// UpdateRequest is the public HTTP input for updating a project.
type UpdateRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	Status      *string `json:"status"`
}

func (r UpdateRequest) toInput() projectservice.UpdateInput {
	return projectservice.UpdateInput{
		Name:        r.Name,
		Description: r.Description,
		Status:      r.Status,
	}
}

type projectResponse struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
}

// RegisterHTTP exposes project routes with HTTP DTO handlers.
func RegisterHTTP(endpoint *httpx.Endpoint, service *projectservice.Service) {
	endpoint.Get("/projects", getProjects(service))
	endpoint.Post("/projects", createProject(service))
	endpoint.Put("/projects", updateProject(service))
	endpoint.Delete("/projects", deleteProject(service))
}

func getProjects(service *projectservice.Service) httpx.Handler {
	return func(ctx *httpx.Context) error {
		id := ctx.Query("id")
		if id == "" {
			projects, err := service.List(ctx.Context())
			if err != nil {
				return err
			}
			return ctx.JSON(http.StatusOK, newProjectResponses(projects))
		}
		projectID, err := parseID(id)
		if err != nil {
			return writeError(ctx, http.StatusBadRequest, err.Error())
		}
		project, err := service.Find(ctx.Context(), projectID)
		if err != nil {
			return writeServiceError(ctx, err)
		}
		return ctx.JSON(http.StatusOK, newProjectResponse(project))
	}
}

func createProject(service *projectservice.Service) httpx.Handler {
	return func(ctx *httpx.Context) error {
		var request CreateRequest
		if err := ctx.JSONBody(&request); err != nil {
			return writeError(ctx, http.StatusBadRequest, "invalid json")
		}
		created, err := service.Create(ctx.Context(), request.toInput())
		if err != nil {
			return writeServiceError(ctx, err)
		}
		return ctx.JSON(http.StatusCreated, newProjectResponse(created))
	}
}

func updateProject(service *projectservice.Service) httpx.Handler {
	return func(ctx *httpx.Context) error {
		id, err := parseID(ctx.Query("id"))
		if err != nil {
			return writeError(ctx, http.StatusBadRequest, err.Error())
		}
		var request UpdateRequest
		if err := ctx.JSONBody(&request); err != nil {
			return writeError(ctx, http.StatusBadRequest, "invalid json")
		}
		updated, err := service.Update(ctx.Context(), id, request.toInput())
		if err != nil {
			return writeServiceError(ctx, err)
		}
		return ctx.JSON(http.StatusOK, newProjectResponse(updated))
	}
}

func deleteProject(service *projectservice.Service) httpx.Handler {
	return func(ctx *httpx.Context) error {
		id, err := parseID(ctx.Query("id"))
		if err != nil {
			return writeError(ctx, http.StatusBadRequest, err.Error())
		}
		if err := service.Delete(ctx.Context(), id); err != nil {
			return writeServiceError(ctx, err)
		}
		return ctx.JSON(http.StatusOK, map[string]bool{"deleted": true})
	}
}

func parseID(raw string) (int64, error) {
	if strings.TrimSpace(raw) == "" {
		return 0, fmt.Errorf("id is required")
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("id must be a positive integer")
	}
	return id, nil
}

func writeServiceError(ctx *httpx.Context, err error) error {
	if errors.Is(err, projectservice.ErrInvalidInput) {
		return writeError(ctx, http.StatusBadRequest, err.Error())
	}
	if errors.Is(err, sql.ErrNoRows) {
		return writeError(ctx, http.StatusNotFound, "project not found")
	}
	return err
}

func writeError(ctx *httpx.Context, status int, message string) error {
	return ctx.JSON(status, map[string]string{"error": message})
}

func newProjectResponses(values []projects.Project) []projectResponse {
	responses := make([]projectResponse, 0, len(values))
	for _, value := range values {
		responses = append(responses, newProjectResponse(value))
	}
	return responses
}

func newProjectResponse(value projects.Project) projectResponse {
	return projectResponse{
		ID:          value.ID,
		Name:        value.Name,
		Description: value.Description,
		Status:      value.Status,
		CreatedAt:   value.CreatedAt,
	}
}
