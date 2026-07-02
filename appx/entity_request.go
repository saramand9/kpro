// File: appx/entity_request.go
// Purpose: Defines protocol-neutral Entity requests and dispatch.

package appx

import (
	"context"
	"fmt"

	"github.com/saramand9/bignet/kpro/dbx"
)

// EntityAction identifies a protocol-neutral entity operation.
type EntityAction string

const (
	// EntityCreate inserts a new entity value.
	EntityCreate EntityAction = "create"
	// EntityUpdate updates an existing entity value.
	EntityUpdate EntityAction = "update"
	// EntityUpsert inserts or updates an entity value.
	EntityUpsert EntityAction = "upsert"
	// EntityDelete removes an entity by id.
	EntityDelete EntityAction = "delete"
	// EntityFind returns one entity by id.
	EntityFind EntityAction = "find"
	// EntityList returns all entities.
	EntityList EntityAction = "list"
)

// EntityRequest is the protocol-neutral input for an Entity operation.
type EntityRequest[T any] struct {
	Action   EntityAction
	ID       any
	Value    *T
	Conflict dbx.ConflictConfig
}

// EntityResponse is the protocol-neutral output of an Entity operation.
type EntityResponse struct {
	Result any
}

// DispatchEntity executes request against entity without knowing the source protocol.
func DispatchEntity[T any](ctx context.Context, entity *Entity[T], request EntityRequest[T]) (EntityResponse, error) {
	if entity == nil {
		return EntityResponse{}, fmt.Errorf("nil entity")
	}
	switch request.Action {
	case EntityCreate:
		if request.Value == nil {
			return EntityResponse{}, fmt.Errorf("entity create requires value")
		}
		value, err := entity.Create(ctx, request.Value)
		return EntityResponse{Result: value}, err
	case EntityUpdate:
		if request.Value == nil {
			return EntityResponse{}, fmt.Errorf("entity update requires value")
		}
		value, err := entity.Update(ctx, request.Value)
		return EntityResponse{Result: value}, err
	case EntityUpsert:
		if request.Value == nil {
			return EntityResponse{}, fmt.Errorf("entity upsert requires value")
		}
		value, err := entity.Upsert(ctx, request.Value, request.Conflict)
		return EntityResponse{Result: value}, err
	case EntityDelete:
		if request.ID == nil || request.ID == "" {
			return EntityResponse{}, fmt.Errorf("entity delete requires id")
		}
		err := entity.Delete(ctx, request.ID)
		return EntityResponse{Result: map[string]bool{"deleted": err == nil}}, err
	case EntityFind:
		if request.ID == nil || request.ID == "" {
			return EntityResponse{}, fmt.Errorf("entity find requires id")
		}
		value, err := entity.Find(ctx, request.ID)
		return EntityResponse{Result: value}, err
	case EntityList:
		values, err := entity.List(ctx)
		return EntityResponse{Result: values}, err
	default:
		return EntityResponse{}, fmt.Errorf("unsupported entity action %q", request.Action)
	}
}
