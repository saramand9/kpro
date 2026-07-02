// File: appx/entity_test.go
// Purpose: Tests model-centric Entity helpers.

package appx

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/saramand9/bignet/kpro/dbx"
	"github.com/saramand9/bignet/kpro/transport/httpx"
)

func TestEntityCRUD(t *testing.T) {
	database := openCRUDTestDatabase(t)
	defer closeCRUDTestDatabase(t, database)
	createCRUDTestTable(t, database)

	entity := NewEntity[crudTestUser](database, EntityConfig{Table: "users"})
	if _, err := entity.Create(context.Background(), &crudTestUser{Name: "Ada", Age: 37}); err != nil {
		t.Fatalf("create: %v", err)
	}
	users, err := entity.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(users) != 1 || users[0].Name != "Ada" {
		t.Fatalf("users = %#v", users)
	}
	if err := entity.Delete(context.Background(), users[0].ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

func TestEntityHTTPMount(t *testing.T) {
	database := openCRUDTestDatabase(t)
	defer closeCRUDTestDatabase(t, database)
	createCRUDTestTable(t, database)

	endpoint := httpx.New(httpx.Config{})
	entity := NewEntity[crudTestUser](database, EntityConfig{Table: "users"})
	BindHTTPEntity(endpoint, entity, HTTPEntityConfig{Path: "/users"})

	req := httptest.NewRequest(http.MethodPost, "/users", bytes.NewBufferString(`{"name":"Ada","age":37}`))
	resp := httptest.NewRecorder()
	endpoint.ServeHTTP(resp, req)
	if resp.Code != http.StatusCreated {
		t.Fatalf("create status/body = %d/%q", resp.Code, resp.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/users", nil)
	resp = httptest.NewRecorder()
	endpoint.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), "Ada") {
		t.Fatalf("list status/body = %d/%q", resp.Code, resp.Body.String())
	}
}

func TestEntityWithExistingStore(t *testing.T) {
	database := openCRUDTestDatabase(t)
	defer closeCRUDTestDatabase(t, database)
	createCRUDTestTable(t, database)

	store := dbx.NewStore[crudTestUser](database, "users")
	entity := NewEntityWithStore(store)
	if entity.Store() != store {
		t.Fatal("entity did not retain store")
	}
	if entity.Service() == nil {
		t.Fatal("entity service is nil")
	}
}
