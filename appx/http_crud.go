// File: appx/http_crud.go
// Purpose: Provides compatibility wrappers for HTTP Entity bindings.

package appx

import "github.com/saramand9/bignet/kpro/transport/httpx"

// HTTPCRUDConfig controls generated CRUD routes.
type HTTPCRUDConfig = HTTPEntityConfig

// HTTPCRUDHandler adapts HTTP requests to Entity operations.
// Use explicit DTO handlers for business APIs that differ from storage models.
type HTTPCRUDHandler[T any] struct {
	entity *Entity[T]
	config HTTPEntityConfig
}

// NewHTTPCRUDHandler creates a generic HTTP CRUD handler.
// Use explicit DTO handlers for business APIs that differ from storage models.
func NewHTTPCRUDHandler[T any](service *CRUDService[T], config HTTPCRUDConfig) *HTTPCRUDHandler[T] {
	return &HTTPCRUDHandler[T]{
		entity: &Entity[T]{service: service},
		config: config,
	}
}

// RegisterHTTP registers list, create, find, update, and delete routes.
func (h *HTTPCRUDHandler[T]) RegisterHTTP(e *httpx.Endpoint) {
	BindHTTPEntity(e, h.entity, h.config)
}
