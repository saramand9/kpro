// File: appx/crud_test.go
// Purpose: Tests generic CRUD service and HTTP handler helpers.

package appx

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/saramand9/bignet/kpro/dbx"
	"github.com/saramand9/bignet/kpro/transport/httpx"
	_ "modernc.org/sqlite"
)

type crudTestUser struct {
	ID   int64  `json:"id" db:"id,pk,auto"`
	Name string `json:"name" db:"name"`
	Age  int    `json:"age" db:"age"`
}

func TestCRUDServiceWithDBXStore(t *testing.T) {
	database := openCRUDTestDatabase(t)
	defer closeCRUDTestDatabase(t, database)
	createCRUDTestTable(t, database)

	service := NewCRUDService[crudTestUser](dbx.NewStore[crudTestUser](database, "users"))
	if _, err := service.Create(context.Background(), &crudTestUser{Name: "Ada", Age: 37}); err != nil {
		t.Fatalf("create: %v", err)
	}
	users, err := service.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(users) != 1 || users[0].Name != "Ada" {
		t.Fatalf("users = %#v", users)
	}
	found, err := service.Find(context.Background(), users[0].ID)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if found.Name != "Ada" {
		t.Fatalf("found = %#v", found)
	}
	if err := service.Delete(context.Background(), users[0].ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	users, err = service.List(context.Background())
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if len(users) != 0 {
		t.Fatalf("users after delete = %#v", users)
	}
}

func TestHTTPCRUDHandlerCreateListAndDelete(t *testing.T) {
	database := openCRUDTestDatabase(t)
	defer closeCRUDTestDatabase(t, database)
	createCRUDTestTable(t, database)

	endpoint := httpx.New(httpx.Config{})
	service := NewCRUDService[crudTestUser](dbx.NewStore[crudTestUser](database, "users"))
	RegisterHTTP(endpoint, NewHTTPCRUDHandler(service, HTTPCRUDConfig{Path: "/users"}))

	createBody := bytes.NewBufferString(`{"name":"Ada","age":37}`)
	createReq := httptest.NewRequest(http.MethodPost, "/users", createBody)
	createResp := httptest.NewRecorder()
	endpoint.ServeHTTP(createResp, createReq)
	if createResp.Code != http.StatusCreated {
		t.Fatalf("create status = %d body = %q", createResp.Code, createResp.Body.String())
	}

	var created crudTestUser
	if err := json.Unmarshal(createResp.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Name != "Ada" {
		t.Fatalf("created = %#v", created)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/users", nil)
	listResp := httptest.NewRecorder()
	endpoint.ServeHTTP(listResp, listReq)
	if listResp.Code != http.StatusOK || !strings.Contains(listResp.Body.String(), "Ada") {
		t.Fatalf("list status/body = %d/%q", listResp.Code, listResp.Body.String())
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/users?id=1", nil)
	deleteResp := httptest.NewRecorder()
	endpoint.ServeHTTP(deleteResp, deleteReq)
	if deleteResp.Code != http.StatusOK {
		t.Fatalf("delete status/body = %d/%q", deleteResp.Code, deleteResp.Body.String())
	}
}

func openCRUDTestDatabase(t *testing.T) *dbx.Database {
	t.Helper()
	database, err := dbx.Open(context.Background(), dbx.Config{
		DriverName:   "sqlite",
		DSN:          ":memory:",
		Dialect:      dbx.SQLite,
		MaxOpenConns: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return database
}

func closeCRUDTestDatabase(t *testing.T, database *dbx.Database) {
	t.Helper()
	if err := database.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func createCRUDTestTable(t *testing.T, database *dbx.Database) {
	t.Helper()
	_, err := database.Writer().ExecContext(context.Background(), `
CREATE TABLE users (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL,
	age INTEGER NOT NULL
)`)
	if err != nil {
		t.Fatal(err)
	}
}
