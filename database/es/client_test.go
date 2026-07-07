package elasticstore

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/saramand9/bignet/kpro/database/common/lifecycle"
)

type searchDoc struct {
	Title string `json:"title"`
}

func TestSearchIndexLifecycle(t *testing.T) {
	var indexed bool
	var deleted bool
	var bulkBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Elastic-Product", "Elasticsearch")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/":
			_, _ = w.Write([]byte(`{"name":"test"}`))
		case r.Method == http.MethodPut && r.URL.Path == "/articles/_doc/a1":
			indexed = true
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"result":"created"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/articles/_doc/a1":
			_, _ = w.Write([]byte(`{"found":true,"_source":{"title":"hello"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/articles/_search":
			_, _ = w.Write([]byte(`{"hits":{"hits":[]}}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/articles/_doc/a1":
			deleted = true
			_, _ = w.Write([]byte(`{"result":"deleted"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/articles/_bulk":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			bulkBody = string(body)
			_, _ = w.Write([]byte(`{"errors":false,"items":[]}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	client, err := Open(context.Background(), Config{Addresses: []string{server.URL}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if health := client.Health(context.Background()); health.Status != lifecycle.StatusOK {
		t.Fatalf("health=%#v", health)
	}

	index := SearchIndex[searchDoc](client, "articles")
	if err := index.Index(context.Background(), "a1", searchDoc{Title: "hello"}); err != nil {
		t.Fatal(err)
	}
	raw, ok, err := index.GetRaw(context.Background(), "a1")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected document")
	}
	var doc searchDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Title != "hello" {
		t.Fatalf("doc=%#v", doc)
	}
	if _, err := index.SearchRaw(context.Background(), map[string]any{"query": map[string]any{"match_all": map[string]any{}}}); err != nil {
		t.Fatal(err)
	}
	if err := index.Delete(context.Background(), "a1"); err != nil {
		t.Fatal(err)
	}
	if err := client.Bulk("articles").Do(context.Background(),
		BulkIndex("a2", searchDoc{Title: "bulk"}),
		BulkDelete("a1"),
	); err != nil {
		t.Fatal(err)
	}
	if !indexed || !deleted {
		t.Fatalf("indexed=%v deleted=%v", indexed, deleted)
	}
	for _, want := range []string{`"index"`, `"delete"`, `"bulk"`} {
		if !strings.Contains(bulkBody, want) {
			t.Fatalf("bulk body %q missing %q", bulkBody, want)
		}
	}
}

func TestBulkReportsItemErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Elastic-Product", "Elasticsearch")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/":
			_, _ = w.Write([]byte(`{"name":"test"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/articles/_bulk":
			_, _ = w.Write([]byte(`{"errors":true,"items":[{"index":{"status":400}}]}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	client, err := Open(context.Background(), Config{Addresses: []string{server.URL}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Bulk("articles").Do(context.Background(), BulkIndex("a1", searchDoc{Title: "bad"})); err == nil {
		t.Fatal("expected bulk error")
	}
}
