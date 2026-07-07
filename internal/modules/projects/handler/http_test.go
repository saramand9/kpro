// File: internal/modules/projects/handler/http_test.go
// Purpose: Tests explicit HTTP DTO handlers for the projects module.

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/saramand9/bignet/kpro/database/dbx"
	"github.com/saramand9/bignet/kpro/internal/modules/projects"
	projectservice "github.com/saramand9/bignet/kpro/internal/modules/projects/service"
	projectstore "github.com/saramand9/bignet/kpro/internal/modules/projects/store"
	"github.com/saramand9/bignet/kpro/transport/httpx"
	_ "modernc.org/sqlite"
)

func TestHTTPCreateUpdateAndFindProject(t *testing.T) {
	database := openHTTPTestDatabase(t)
	defer closeHTTPTestDatabase(t, database)

	endpoint := httpx.New(httpx.Config{})
	RegisterHTTP(endpoint, projectservice.NewWithStore(projectstore.NewDBX(database)))

	invalid := executeHTTPTestRequest(endpoint, http.MethodPost, "/projects", `{"status":"active"}`)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid status = %d, want %d", invalid.Code, http.StatusBadRequest)
	}

	resp := executeHTTPTestRequest(endpoint, http.MethodPost, "/projects", `{"name":" Apollo ","description":" Launch ","status":""}`)
	if resp.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %q", resp.Code, resp.Body.String())
	}
	var created projects.Project
	if err := json.Unmarshal(resp.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == 0 {
		t.Fatalf("created id = %d, want database id", created.ID)
	}
	if created.Name != "Apollo" || created.Description != "Launch" || created.Status != "active" {
		t.Fatalf("created = %#v", created)
	}
	if created.CreatedAt == "" {
		t.Fatalf("created_at was not returned: %#v", created)
	}

	updated := executeHTTPTestRequest(endpoint, http.MethodPut, "/projects?id=1", `{"status":"PAUSED"}`)
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), `"status":"paused"`) {
		t.Fatalf("update status/body = %d/%q", updated.Code, updated.Body.String())
	}

	found := executeHTTPTestRequest(endpoint, http.MethodGet, "/projects?id=1", "")
	if found.Code != http.StatusOK || !strings.Contains(found.Body.String(), `"name":"Apollo"`) {
		t.Fatalf("find status/body = %d/%q", found.Code, found.Body.String())
	}
}

func openHTTPTestDatabase(t *testing.T) *dbx.Database {
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
	if err := projects.Init(context.Background(), database); err != nil {
		t.Fatal(err)
	}
	return database
}

func closeHTTPTestDatabase(t *testing.T, database *dbx.Database) {
	t.Helper()
	if err := database.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func executeHTTPTestRequest(endpoint *httpx.Endpoint, method, target, body string) *httptest.ResponseRecorder {
	var reader *bytes.Buffer
	if body == "" {
		reader = bytes.NewBuffer(nil)
	} else {
		reader = bytes.NewBufferString(body)
	}
	req := httptest.NewRequest(method, target, reader)
	recorder := httptest.NewRecorder()
	endpoint.ServeHTTP(recorder, req)
	return recorder
}
