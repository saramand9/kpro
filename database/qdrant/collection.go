package qdrantstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/saramand9/kpro/database/common/readmodels"
)

// CollectionView implements a typed Qdrant vector collection.
type CollectionView[T any] struct {
	client *Client
	name   string
}

var _ readmodels.VectorCollection[any] = (*CollectionView[any])(nil)

// VectorConfig describes a single unnamed Qdrant vector configuration.
type VectorConfig struct {
	Size     uint64 `json:"size"`
	Distance string `json:"distance"`
}

// Point is a Qdrant point upsert payload.
type Point[T any] struct {
	ID      string    `json:"id"`
	Vector  []float32 `json:"vector,omitempty"`
	Payload T         `json:"payload,omitempty"`
}

// ScoredPoint is one Qdrant search result.
type ScoredPoint[T any] struct {
	ID      any     `json:"id"`
	Score   float64 `json:"score"`
	Payload T       `json:"payload,omitempty"`
	Vector  any     `json:"vector,omitempty"`
}

// Create creates the collection using a single vector config or native vectors body.
func (c *CollectionView[T]) Create(ctx context.Context, vectors any) error {
	if err := c.validate(); err != nil {
		return err
	}
	if vectors == nil {
		return invalidRequest("missing vectors")
	}
	_, err := c.client.do(ctx, http.MethodPut, c.path(), nil, map[string]any{"vectors": vectors})
	return err
}

// Drop deletes the collection.
func (c *CollectionView[T]) Drop(ctx context.Context) error {
	if err := c.validate(); err != nil {
		return err
	}
	_, err := c.client.do(ctx, http.MethodDelete, c.path(), nil, nil)
	return err
}

// Upsert implements readmodels.VectorCollection.
func (c *CollectionView[T]) Upsert(ctx context.Context, id string, vector []float32, payload T) error {
	if err := c.validate(); err != nil {
		return err
	}
	if id == "" {
		return invalidRequest("point id is required")
	}
	query := url.Values{"wait": []string{"true"}}
	_, err := c.client.do(ctx, http.MethodPut, c.path()+"/points", query, map[string]any{
		"points": []Point[T]{{ID: id, Vector: vector, Payload: payload}},
	})
	return err
}

// Delete implements readmodels.VectorCollection.
func (c *CollectionView[T]) Delete(ctx context.Context, id string) error {
	if err := c.validate(); err != nil {
		return err
	}
	if id == "" {
		return invalidRequest("point id is required")
	}
	_, err := c.client.do(ctx, http.MethodPost, c.path()+"/points/delete", url.Values{"wait": []string{"true"}}, map[string]any{
		"points": []string{id},
	})
	return err
}

// SearchRaw executes a native Qdrant vector search request.
func (c *CollectionView[T]) SearchRaw(ctx context.Context, request any) (json.RawMessage, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	body, err := c.client.do(ctx, http.MethodPost, c.path()+"/points/search", nil, request)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(body), nil
}

func (c *CollectionView[T]) validate() error {
	if c == nil || c.client == nil {
		return sql.ErrConnDone
	}
	if c.name == "" {
		return invalidRequest("collection is required")
	}
	return nil
}

func (c *CollectionView[T]) path() string {
	return "/collections/" + url.PathEscape(c.name)
}
