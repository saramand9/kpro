// Package views defines query-model capabilities for projection outputs.
package views

import (
	"context"
	"time"
)

// Cache is a typed key-value view with TTL.
type Cache[T any] interface {
	Get(ctx context.Context, key string) (T, bool, error)
	Set(ctx context.Context, key string, value T, ttl time.Duration) error
	Delete(ctx context.Context, key string) error
}

// SearchIndex stores documents for text search.
type SearchIndex[T any] interface {
	Index(ctx context.Context, id string, document T) error
	Delete(ctx context.Context, id string) error
}

// VectorCollection stores vectors with typed payloads.
type VectorCollection[T any] interface {
	Upsert(ctx context.Context, id string, vector []float32, payload T) error
	Delete(ctx context.Context, id string) error
}

// Graph stores relationship-oriented views.
type Graph interface {
	UpsertNode(ctx context.Context, node Node) error
	UpsertEdge(ctx context.Context, edge Edge) error
}

// Node is a graph node view.
type Node struct {
	Label string
	ID    string
	Props map[string]any
}

// Edge is a graph edge view.
type Edge struct {
	Type  string
	From  NodeRef
	To    NodeRef
	Props map[string]any
}

// NodeRef identifies a graph node.
type NodeRef struct {
	Label string
	ID    string
}

// AnalyticsTable accepts append-oriented analytical facts.
type AnalyticsTable[T any] interface {
	Write(ctx context.Context, value T) error
	WriteBatch(ctx context.Context, values []T) error
}

// Measurement accepts time-series points.
type Measurement[T any] interface {
	WritePoint(ctx context.Context, point T) error
	WriteBatch(ctx context.Context, points []T) error
}
