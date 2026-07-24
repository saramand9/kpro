package elasticstore

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/saramand9/kpro/database/common/lifecycle"
)

type searchDoc struct {
	Title string `json:"title"`
}

type closeCountingTransport struct {
	base   http.RoundTripper
	closes atomic.Int32
}

func (t *closeCountingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return t.base.RoundTrip(request)
}

func (t *closeCountingTransport) CloseIdleConnections() {
	t.closes.Add(1)
	if closer, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
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
	if !client.ownsTransport || client.transport == nil {
		t.Fatal("implicitly created transport is not lifecycle-owned")
	}
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

func TestCloseDoesNotCloseCallerOwnedTransport(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Elastic-Product", "Elasticsearch")
		_, _ = w.Write([]byte(`{"name":"test"}`))
	}))
	defer server.Close()

	base := http.DefaultTransport.(*http.Transport).Clone()
	transport := &closeCountingTransport{base: base}
	client, err := Open(context.Background(), Config{
		Addresses: []string{server.URL},
		Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	if client.ownsTransport {
		t.Fatal("caller-provided transport marked as owned")
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if got := transport.closes.Load(); got != 0 {
		t.Fatalf("caller-owned transport closes=%d, want 0", got)
	}
	base.CloseIdleConnections()

	owned := &Client{ownsTransport: true, transport: transport}
	if err := owned.Close(); err != nil {
		t.Fatal(err)
	}
	if got := transport.closes.Load(); got != 1 {
		t.Fatalf("owned transport closes=%d, want 1", got)
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
