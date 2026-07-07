// File: internal/modules/users/handler/http_test.go
// Purpose: Tests explicit HTTP DTO handlers for the users module.

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gormstore "github.com/saramand9/bignet/kpro/database/gorm"
	"github.com/saramand9/bignet/kpro/internal/modules/users"
	userservice "github.com/saramand9/bignet/kpro/internal/modules/users/service"
	userstore "github.com/saramand9/bignet/kpro/internal/modules/users/store"
	"github.com/saramand9/bignet/kpro/transport/httpx"
	"gorm.io/driver/sqlite"

	_ "modernc.org/sqlite"
)

func TestHTTPCreateUsesDTOAndReturnsStoredUser(t *testing.T) {
	database := openHTTPTestDatabase(t)
	defer closeHTTPTestDatabase(t, database)

	endpoint := httpx.New(httpx.Config{})
	RegisterHTTP(endpoint, userservice.NewWithStore(userstore.NewGORM(database)))

	invalid := executeHTTPTestRequest(endpoint, http.MethodPost, "/users", `{"age":37}`)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid status = %d, want %d", invalid.Code, http.StatusBadRequest)
	}

	resp := executeHTTPTestRequest(endpoint, http.MethodPost, "/users", `{"name":" Ada ","age":37}`)
	if resp.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %q", resp.Code, resp.Body.String())
	}
	var created users.User
	if err := json.Unmarshal(resp.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == 0 {
		t.Fatalf("created id = %d, want database id", created.ID)
	}
	if created.Name != "Ada" || created.Age != 37 {
		t.Fatalf("created = %#v", created)
	}
	if created.CreatedAt.IsZero() {
		t.Fatalf("created_at was not returned: %#v", created)
	}

	found := executeHTTPTestRequest(endpoint, http.MethodGet, "/users?id=1", "")
	if found.Code != http.StatusOK || !strings.Contains(found.Body.String(), `"name":"Ada"`) {
		t.Fatalf("find status/body = %d/%q", found.Code, found.Body.String())
	}
}

func openHTTPTestDatabase(t *testing.T) *gormstore.Database {
	t.Helper()
	database, err := gormstore.Open(context.Background(), gormstore.Config{
		Dialector:    sqlite.Dialector{DriverName: "sqlite", DSN: ":memory:"},
		MaxOpenConns: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := users.Init(context.Background(), database); err != nil {
		t.Fatal(err)
	}
	return database
}

func closeHTTPTestDatabase(t *testing.T, database *gormstore.Database) {
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
