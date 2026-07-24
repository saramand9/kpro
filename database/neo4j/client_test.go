package neo4jstore

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/saramand9/kpro/database/common/lifecycle"
	"github.com/saramand9/kpro/database/common/readmodels"
)

func TestOpenValidatesConfig(t *testing.T) {
	if _, err := Open(context.Background(), Config{}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("missing config err=%v, want ErrInvalidConfig", err)
	}
	if _, err := Open(context.Background(), Config{URI: "bolt://127.0.0.1:7687", Driver: fakeDriver{}}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("mixed config err=%v, want ErrInvalidConfig", err)
	}
}

func TestOpenSkipVerifyCreatesResource(t *testing.T) {
	client, err := Open(context.Background(), Config{
		Name:                 "graph",
		URI:                  "bolt://127.0.0.1:1",
		Database:             "neo4j",
		SkipVerifyConnection: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	if client.Name() != "graph" || client.Kind() != lifecycle.KindGraph {
		t.Fatalf("resource=%s/%s", client.Name(), client.Kind())
	}
	if client.Database() != "neo4j" {
		t.Fatalf("database=%q, want neo4j", client.Database())
	}
	if client.Driver() == nil {
		t.Fatal("missing driver")
	}
}

func TestGraphUpsertNodeAndEdge(t *testing.T) {
	executor := &fakeGraphExecutor{}
	graph := &Graph{executor: executor}

	err := graph.UpsertNode(context.Background(), readmodels.Node{
		Label: "User",
		ID:    "u1",
		Props: map[string]any{"id": "bad", "name": "Ada"},
	})
	if err != nil {
		t.Fatal(err)
	}
	err = graph.UpsertEdge(context.Background(), readmodels.Edge{
		Type: "OWNS",
		From: readmodels.NodeRef{
			Label: "User",
			ID:    "u1",
		},
		To: readmodels.NodeRef{
			Label: "Project",
			ID:    "p1",
		},
		Props: map[string]any{"role": "admin"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(executor.writes) != 2 {
		t.Fatalf("writes=%d, want 2", len(executor.writes))
	}
	nodeWrite := executor.writes[0]
	if nodeWrite.cypher != "MERGE (n:`User` {id: $id}) SET n += $props" {
		t.Fatalf("node cypher=%q", nodeWrite.cypher)
	}
	if nodeWrite.params["id"] != "u1" {
		t.Fatalf("node id=%#v", nodeWrite.params["id"])
	}
	nodeProps := nodeWrite.params["props"].(map[string]any)
	if _, ok := nodeProps["id"]; ok {
		t.Fatalf("node props should not contain id: %#v", nodeProps)
	}
	if nodeProps["name"] != "Ada" {
		t.Fatalf("node props=%#v", nodeProps)
	}

	edgeWrite := executor.writes[1]
	for _, want := range []string{"MERGE (from:`User`", "MERGE (to:`Project`", "MERGE (from)-[r:`OWNS`]->(to)"} {
		if !strings.Contains(edgeWrite.cypher, want) {
			t.Fatalf("edge cypher %q missing %q", edgeWrite.cypher, want)
		}
	}
	if edgeWrite.params["fromID"] != "u1" || edgeWrite.params["toID"] != "p1" {
		t.Fatalf("edge params=%#v", edgeWrite.params)
	}
}

func TestGraphBatchUpsert(t *testing.T) {
	executor := &fakeGraphExecutor{}
	graph := &Graph{executor: executor}

	if err := graph.BatchUpsertNodes(context.Background(),
		readmodels.Node{Label: "User", ID: "u1"},
		readmodels.Node{Label: "Project", ID: "p1"},
	); err != nil {
		t.Fatal(err)
	}
	if err := graph.BatchUpsertEdges(context.Background(), readmodels.Edge{
		Type: "OWNS",
		From: readmodels.NodeRef{Label: "User", ID: "u1"},
		To:   readmodels.NodeRef{Label: "Project", ID: "p1"},
	}); err != nil {
		t.Fatal(err)
	}

	if len(executor.writes) != 3 {
		t.Fatalf("writes=%d, want 3", len(executor.writes))
	}
}
func TestGraphRejectsUnsafeIdentifiers(t *testing.T) {
	graph := &Graph{executor: &fakeGraphExecutor{}}
	err := graph.UpsertNode(context.Background(), readmodels.Node{
		Label: "User) DETACH DELETE n //",
		ID:    "u1",
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("unsafe label err=%v, want ErrInvalidRequest", err)
	}
	err = graph.UpsertEdge(context.Background(), readmodels.Edge{
		Type: "HAS ROLE",
		From: readmodels.NodeRef{
			Label: "User",
			ID:    "u1",
		},
		To: readmodels.NodeRef{
			Label: "Role",
			ID:    "r1",
		},
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("unsafe type err=%v, want ErrInvalidRequest", err)
	}
}

func TestGraphValidatesRequiredFields(t *testing.T) {
	graph := &Graph{executor: &fakeGraphExecutor{}}
	if err := graph.UpsertNode(context.Background(), readmodels.Node{Label: "User"}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("missing node id err=%v, want ErrInvalidRequest", err)
	}
	if err := graph.UpsertEdge(context.Background(), readmodels.Edge{Type: "OWNS"}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("missing edge endpoint err=%v, want ErrInvalidRequest", err)
	}
	if err := (*Graph)(nil).WriteRaw(context.Background(), "RETURN 1", nil); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("nil graph err=%v, want ErrInvalidConfig", err)
	}
}

type graphWrite struct {
	cypher string
	params map[string]any
}

type fakeGraphExecutor struct {
	writes []graphWrite
}

func (f *fakeGraphExecutor) executeWrite(ctx context.Context, cypher string, params map[string]any) error {
	f.writes = append(f.writes, graphWrite{cypher: cypher, params: params})
	return nil
}

type fakeDriver struct{}

func (fakeDriver) ExecuteQueryBookmarkManager() neo4j.BookmarkManager { return nil }
func (fakeDriver) Target() url.URL                                    { return url.URL{} }
func (fakeDriver) NewSession(context.Context, neo4j.SessionConfig) neo4j.SessionWithContext {
	return nil
}
func (fakeDriver) VerifyConnectivity(context.Context) error                     { return nil }
func (fakeDriver) VerifyAuthentication(context.Context, *neo4j.AuthToken) error { return nil }
func (fakeDriver) Close(context.Context) error                                  { return nil }
func (fakeDriver) IsEncrypted() bool                                            { return false }
func (fakeDriver) GetServerInfo(context.Context) (neo4j.ServerInfo, error) {
	return nil, nil
}
