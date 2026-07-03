// File: appx/entity_http.go
// Purpose: Adapts HTTP requests into protocol-neutral Entity requests.

package appx

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/saramand9/bignet/kpro/transport/httpx"
)

// HTTPEntityConfig controls HTTP routes bound to an Entity.
type HTTPEntityConfig struct {
	// Path is the collection path, for example "/users".
	Path string
	// IDQuery is the query parameter used for single-row operations. Empty uses "id".
	IDQuery string
}

// BindHTTPEntity binds HTTP routes to a protocol-neutral Entity.
// It is intended for simple internal CRUD surfaces where HTTP JSON matches T.
// Business APIs should prefer explicit DTO handlers.
func BindHTTPEntity[T any](endpoint *httpx.Endpoint, entity *Entity[T], config HTTPEntityConfig) {
	adapter := &httpEntityAdapter[T]{
		entity: entity,
		config: normalizeHTTPEntityConfig(config),
	}
	endpoint.Get(adapter.config.Path, adapter.get)
	endpoint.Post(adapter.config.Path, adapter.create)
	endpoint.Put(adapter.config.Path, adapter.update)
	endpoint.Delete(adapter.config.Path, adapter.delete)
}

type httpEntityAdapter[T any] struct {
	entity *Entity[T]
	config HTTPEntityConfig
}

func normalizeHTTPEntityConfig(config HTTPEntityConfig) HTTPEntityConfig {
	if config.IDQuery == "" {
		config.IDQuery = "id"
	}
	return config
}

func (a *httpEntityAdapter[T]) get(ctx *httpx.Context) error {
	id := ctx.Query(a.config.IDQuery)
	action := EntityList
	if id != "" {
		action = EntityFind
	}
	return a.dispatch(ctx, EntityRequest[T]{
		Action: action,
		ID:     id,
	})
}

func (a *httpEntityAdapter[T]) create(ctx *httpx.Context) error {
	value, ok := decodeHTTPValue[T](ctx)
	if !ok {
		return nil
	}
	return a.dispatch(ctx, EntityRequest[T]{
		Action: EntityCreate,
		Value:  &value,
	})
}

func (a *httpEntityAdapter[T]) update(ctx *httpx.Context) error {
	value, ok := decodeHTTPValue[T](ctx)
	if !ok {
		return nil
	}
	return a.dispatch(ctx, EntityRequest[T]{
		Action: EntityUpdate,
		Value:  &value,
	})
}

func (a *httpEntityAdapter[T]) delete(ctx *httpx.Context) error {
	id := ctx.Query(a.config.IDQuery)
	if id == "" {
		return ctx.JSON(http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("missing query %q", a.config.IDQuery)})
	}
	return a.dispatch(ctx, EntityRequest[T]{
		Action: EntityDelete,
		ID:     id,
	})
}

func (a *httpEntityAdapter[T]) dispatch(ctx *httpx.Context, request EntityRequest[T]) error {
	response, err := DispatchEntity(ctx.Context(), a.entity, request)
	if err != nil {
		return err
	}
	status := http.StatusOK
	if request.Action == EntityCreate {
		status = http.StatusCreated
	}
	return ctx.JSON(status, response.Result)
}

func decodeHTTPValue[T any](ctx *httpx.Context) (T, bool) {
	var value T
	body, err := ctx.Body()
	if err != nil {
		_ = ctx.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return value, false
	}
	if err := json.Unmarshal(body, &value); err != nil {
		_ = ctx.JSON(http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return value, false
	}
	return value, true
}
