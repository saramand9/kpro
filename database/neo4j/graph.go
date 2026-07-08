package neo4jstore

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/saramand9/bignet/kpro/database/common/readmodels"
)

var graphIdentifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type graphExecutor interface {
	executeWrite(ctx context.Context, cypher string, params map[string]any) error
}

// Graph implements readmodels.Graph using Cypher MERGE operations.
type Graph struct {
	executor graphExecutor
}

var _ readmodels.Graph = (*Graph)(nil)

// NewGraph creates a graph helper from a Neo4j client.
func NewGraph(client *Client) *Graph {
	return &Graph{executor: client}
}

// UpsertNode implements readmodels.Graph.
func (g *Graph) UpsertNode(ctx context.Context, node readmodels.Node) error {
	cypher, params, err := buildUpsertNode(node)
	if err != nil {
		return err
	}
	return g.write(ctx, cypher, params)
}

// UpsertEdge implements readmodels.Graph.
func (g *Graph) UpsertEdge(ctx context.Context, edge readmodels.Edge) error {
	cypher, params, err := buildUpsertEdge(edge)
	if err != nil {
		return err
	}
	return g.write(ctx, cypher, params)
}

// WriteRaw executes a caller-owned write query through the graph executor.
func (g *Graph) WriteRaw(ctx context.Context, cypher string, params map[string]any) error {
	if strings.TrimSpace(cypher) == "" {
		return fmt.Errorf("%w: missing cypher", ErrInvalidRequest)
	}
	return g.write(ctx, cypher, params)
}

func (g *Graph) write(ctx context.Context, cypher string, params map[string]any) error {
	if g == nil || g.executor == nil {
		return fmt.Errorf("%w: nil graph executor", ErrInvalidConfig)
	}
	return g.executor.executeWrite(contextOrBackground(ctx), cypher, params)
}

func buildUpsertNode(node readmodels.Node) (string, map[string]any, error) {
	label, err := quoteGraphIdentifier(node.Label)
	if err != nil {
		return "", nil, fmt.Errorf("%w: node label %q", err, node.Label)
	}
	id := strings.TrimSpace(node.ID)
	if id == "" {
		return "", nil, fmt.Errorf("%w: node id is required", ErrInvalidRequest)
	}
	cypher := fmt.Sprintf("MERGE (n:%s {id: $id}) SET n += $props", label)
	params := map[string]any{
		"id":    id,
		"props": copyGraphProps(node.Props, "id"),
	}
	return cypher, params, nil
}

func buildUpsertEdge(edge readmodels.Edge) (string, map[string]any, error) {
	relationshipType, err := quoteGraphIdentifier(edge.Type)
	if err != nil {
		return "", nil, fmt.Errorf("%w: edge type %q", err, edge.Type)
	}
	fromLabel, err := quoteGraphIdentifier(edge.From.Label)
	if err != nil {
		return "", nil, fmt.Errorf("%w: from label %q", err, edge.From.Label)
	}
	toLabel, err := quoteGraphIdentifier(edge.To.Label)
	if err != nil {
		return "", nil, fmt.Errorf("%w: to label %q", err, edge.To.Label)
	}
	fromID := strings.TrimSpace(edge.From.ID)
	toID := strings.TrimSpace(edge.To.ID)
	if fromID == "" {
		return "", nil, fmt.Errorf("%w: from id is required", ErrInvalidRequest)
	}
	if toID == "" {
		return "", nil, fmt.Errorf("%w: to id is required", ErrInvalidRequest)
	}
	cypher := fmt.Sprintf(
		"MERGE (from:%s {id: $fromID}) MERGE (to:%s {id: $toID}) MERGE (from)-[r:%s]->(to) SET r += $props",
		fromLabel,
		toLabel,
		relationshipType,
	)
	params := map[string]any{
		"fromID": fromID,
		"toID":   toID,
		"props":  copyGraphProps(edge.Props),
	}
	return cypher, params, nil
}

func quoteGraphIdentifier(value string) (string, error) {
	value = strings.TrimSpace(value)
	if !graphIdentifierPattern.MatchString(value) {
		return "", ErrInvalidRequest
	}
	return "`" + value + "`", nil
}

func copyGraphProps(props map[string]any, skipKeys ...string) map[string]any {
	copied := make(map[string]any, len(props))
	for key, value := range props {
		if shouldSkipGraphProp(key, skipKeys) {
			continue
		}
		copied[key] = value
	}
	return copied
}

func shouldSkipGraphProp(key string, skipKeys []string) bool {
	for _, skip := range skipKeys {
		if key == skip {
			return true
		}
	}
	return false
}
