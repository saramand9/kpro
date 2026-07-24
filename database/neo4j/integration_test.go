//go:build neo4jintegration

package neo4jstore

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/saramand9/kpro/database/common/readmodels"
)

func TestNeo4jIntegrationGraphUpsert(t *testing.T) {
	uri := os.Getenv("KPRO_TEST_NEO4J_URI")
	if uri == "" {
		t.Skip("set KPRO_TEST_NEO4J_URI to run Neo4j integration tests")
	}
	ctx := context.Background()
	client, err := Open(ctx, Config{
		URI:      uri,
		Username: os.Getenv("KPRO_TEST_NEO4J_USER"),
		Password: os.Getenv("KPRO_TEST_NEO4J_PASSWORD"),
		Database: os.Getenv("KPRO_TEST_NEO4J_DATABASE"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	idSuffix := fmt.Sprintf("%d", time.Now().UnixNano())
	userID := "user-" + idSuffix
	projectID := "project-" + idSuffix
	t.Cleanup(func() {
		_, _ = client.Execute(context.Background(), `
MATCH (n)
WHERE n.id IN $ids
DETACH DELETE n
`, map[string]any{"ids": []string{userID, projectID}})
	})

	graph := client.Graph()
	if err := graph.UpsertNode(ctx, readmodels.Node{
		Label: "KproTestUser",
		ID:    userID,
		Props: map[string]any{"name": "Ada"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := graph.UpsertEdge(ctx, readmodels.Edge{
		Type: "OWNS",
		From: readmodels.NodeRef{
			Label: "KproTestUser",
			ID:    userID,
		},
		To: readmodels.NodeRef{
			Label: "KproTestProject",
			ID:    projectID,
		},
		Props: map[string]any{"role": "admin"},
	}); err != nil {
		t.Fatal(err)
	}

	result, err := client.Execute(ctx, `
MATCH (u:KproTestUser {id: $userID})-[r:OWNS]->(p:KproTestProject {id: $projectID})
RETURN u.name AS name, r.role AS role
`, map[string]any{"userID": userID, "projectID": projectID})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Records) != 1 {
		t.Fatalf("records=%d, want 1", len(result.Records))
	}
	record := result.Records[0]
	name, _ := record.Get("name")
	role, _ := record.Get("role")
	if name != "Ada" || role != "admin" {
		t.Fatalf("record name=%#v role=%#v", name, role)
	}
}
