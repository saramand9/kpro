// Package neo4jstore adapts Neo4j as a graph read-model resource.
package neo4jstore

import "errors"

var (
	// ErrInvalidConfig reports invalid Neo4j adapter configuration.
	ErrInvalidConfig = errors.New("invalid neo4j config")
	// ErrInvalidRequest reports invalid graph node, edge, label, or relationship input.
	ErrInvalidRequest = errors.New("invalid neo4j request")
)
