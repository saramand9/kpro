// File: appx/entity_request_test.go
// Purpose: Tests protocol-neutral Entity request dispatch.

package appx

import (
	"context"
	"testing"
)

func TestDispatchEntityCreateFindDelete(t *testing.T) {
	database := openCRUDTestDatabase(t)
	defer closeCRUDTestDatabase(t, database)
	createCRUDTestTable(t, database)

	entity := NewEntity[crudTestUser](database, EntityConfig{Table: "users"})
	createResp, err := DispatchEntity(context.Background(), entity, EntityRequest[crudTestUser]{
		Action: EntityCreate,
		Value:  &crudTestUser{Name: "Ada", Age: 37},
	})
	if err != nil {
		t.Fatalf("dispatch create: %v", err)
	}
	created, ok := createResp.Result.(crudTestUser)
	if !ok || created.Name != "Ada" {
		t.Fatalf("created response = %#v", createResp.Result)
	}

	listResp, err := DispatchEntity(context.Background(), entity, EntityRequest[crudTestUser]{Action: EntityList})
	if err != nil {
		t.Fatalf("dispatch list: %v", err)
	}
	users, ok := listResp.Result.([]crudTestUser)
	if !ok || len(users) != 1 {
		t.Fatalf("list response = %#v", listResp.Result)
	}

	deleteResp, err := DispatchEntity(context.Background(), entity, EntityRequest[crudTestUser]{
		Action: EntityDelete,
		ID:     users[0].ID,
	})
	if err != nil {
		t.Fatalf("dispatch delete: %v", err)
	}
	deleted, ok := deleteResp.Result.(map[string]bool)
	if !ok || !deleted["deleted"] {
		t.Fatalf("delete response = %#v", deleteResp.Result)
	}
}
