// File: examples/dbx-http/server/internal/users/http.go
// Purpose: Provides explicit HTTP DTO handlers for the dbx user example.

package users

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/saramand9/bignet/kpro/transport/httpx"
)

// CreateRequest is the public HTTP input for creating a user.
type CreateRequest struct {
	Name string `json:"name"`
	Age  int    `json:"age"`
}

func (r CreateRequest) toInput() CreateInput {
	return CreateInput{
		Name: r.Name,
		Age:  r.Age,
	}
}

// UpdateRequest is the public HTTP input for updating a user.
type UpdateRequest struct {
	Name *string `json:"name"`
	Age  *int    `json:"age"`
}

func (r UpdateRequest) toInput() UpdateInput {
	return UpdateInput{
		Name: r.Name,
		Age:  r.Age,
	}
}

// RegisterHTTP exposes user routes with HTTP DTO handlers.
func RegisterHTTP(endpoint *httpx.Endpoint, service *Service) {
	endpoint.Get("/users", getUsers(service))
	endpoint.Post("/users", createUser(service))
	endpoint.Put("/users", updateUser(service))
	endpoint.Delete("/users", deleteUser(service))
}

func getUsers(service *Service) httpx.Handler {
	return func(ctx *httpx.Context) error {
		id := ctx.Query("id")
		if id == "" {
			users, err := service.List(ctx.Context())
			if err != nil {
				return err
			}
			return ctx.JSON(http.StatusOK, users)
		}
		userID, err := parseID(id)
		if err != nil {
			return writeError(ctx, http.StatusBadRequest, err.Error())
		}
		user, err := service.Find(ctx.Context(), userID)
		if err != nil {
			return writeServiceError(ctx, err)
		}
		return ctx.JSON(http.StatusOK, user)
	}
}

func createUser(service *Service) httpx.Handler {
	return func(ctx *httpx.Context) error {
		var request CreateRequest
		if err := ctx.JSONBody(&request); err != nil {
			return writeError(ctx, http.StatusBadRequest, "invalid json")
		}
		created, err := service.Create(ctx.Context(), request.toInput())
		if err != nil {
			return writeServiceError(ctx, err)
		}
		return ctx.JSON(http.StatusCreated, created)
	}
}

func updateUser(service *Service) httpx.Handler {
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
		return ctx.JSON(http.StatusOK, updated)
	}
}

func deleteUser(service *Service) httpx.Handler {
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
	if errors.Is(err, ErrInvalidInput) {
		return writeError(ctx, http.StatusBadRequest, err.Error())
	}
	if errors.Is(err, sql.ErrNoRows) {
		return writeError(ctx, http.StatusNotFound, "user not found")
	}
	return err
}

func writeError(ctx *httpx.Context, status int, message string) error {
	return ctx.JSON(status, map[string]string{"error": message})
}
