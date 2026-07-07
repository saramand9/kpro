package qdrantstore

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/saramand9/bignet/kpro/database/contracts/lifecycle"
)

type qdrantRequest struct {
	Method string
	Path   string
	Query  string
	APIKey string
	Body   string
}

type qdrantLog struct {
	mu       sync.Mutex
	requests []qdrantRequest
}

func TestCollectionLifecycleAndSearch(t *testing.T) {
	server, log := newQdrantServer(t)
	client, err := Open(context.Background(), Config{URL: server.URL + "/", APIKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}

	if health := client.Health(context.Background()); health.Status != lifecycle.StatusOK {
		t.Fatalf("health=%#v", health)
	}

	collection := Collection[searchPayload](client, "articles")
	if err := collection.Create(context.Background(), VectorConfig{Size: 3, Distance: "Cosine"}); err != nil {
		t.Fatal(err)
	}
	if err := collection.Upsert(context.Background(), "a1", []float32{0.1, 0.2, 0.3}, searchPayload{Title: "hello"}); err != nil {
		t.Fatal(err)
	}
	raw, err := collection.SearchRaw(context.Background(), map[string]any{
		"vector":       []float32{0.1, 0.2, 0.3},
		"limit":        1,
		"with_payload": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(raw) || !strings.Contains(string(raw), "hello") {
		t.Fatalf("search raw=%s", raw)
	}
	if err := collection.Delete(context.Background(), "a1"); err != nil {
		t.Fatal(err)
	}
	if err := collection.Drop(context.Background()); err != nil {
		t.Fatal(err)
	}

	requests := qdrantRequests(log)
	for _, request := range requests {
		if request.APIKey != "secret" {
			t.Fatalf("request %#v missing api key", request)
		}
	}
	upsert := findQdrantRequest(requests, http.MethodPut, "/collections/articles/points")
	if upsert.Query != "wait=true" || !strings.Contains(upsert.Body, "hello") {
		t.Fatalf("upsert request=%#v", upsert)
	}
	if !hasQdrantRequest(requests, http.MethodPost, "/collections/articles/points/search") {
		t.Fatalf("missing search request: %#v", requests)
	}
	if !hasQdrantRequest(requests, http.MethodPost, "/collections/articles/points/delete") {
		t.Fatalf("missing point delete request: %#v", requests)
	}
}

func TestCollectionValidatesRequest(t *testing.T) {
	if _, err := Open(context.Background(), Config{}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("missing config err=%v, want ErrInvalidConfig", err)
	}
	server, _ := newQdrantServer(t)
	client, err := Open(context.Background(), Config{URL: server.URL, SkipHealthCheck: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Collection("").Create(context.Background(), VectorConfig{}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("missing collection err=%v, want ErrInvalidRequest", err)
	}
	if err := client.Collection("articles").Create(context.Background(), nil); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("missing vector config err=%v, want ErrInvalidRequest", err)
	}
	if err := client.Collection("articles").Upsert(context.Background(), "", []float32{1}, map[string]any{}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("missing point id err=%v, want ErrInvalidRequest", err)
	}
}

func TestQdrantResponseError(t *testing.T) {
	server, _ := newQdrantServer(t)
	client, err := Open(context.Background(), Config{URL: server.URL, SkipHealthCheck: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Collection("missing").SearchRaw(context.Background(), map[string]any{"limit": 1})
	var responseErr *ResponseError
	if !errors.As(err, &responseErr) || responseErr.StatusCode != http.StatusNotFound {
		t.Fatalf("response err=%#v", err)
	}
}

type searchPayload struct {
	Title string `json:"title"`
}

func newQdrantServer(t *testing.T) (*httptest.Server, *qdrantLog) {
	t.Helper()
	log := &qdrantLog{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		log.mu.Lock()
		log.requests = append(log.requests, qdrantRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Query:  r.URL.RawQuery,
			APIKey: r.Header.Get("api-key"),
			Body:   string(body),
		})
		log.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/":
			_, _ = io.WriteString(w, `{"title":"qdrant"}`)
		case r.Method == http.MethodPut && r.URL.Path == "/collections/articles":
			_, _ = io.WriteString(w, `{"result":true,"status":"ok"}`)
		case r.Method == http.MethodDelete && r.URL.Path == "/collections/articles":
			_, _ = io.WriteString(w, `{"result":true,"status":"ok"}`)
		case r.Method == http.MethodPut && r.URL.Path == "/collections/articles/points":
			_, _ = io.WriteString(w, `{"result":{"operation_id":1,"status":"completed"},"status":"ok"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/collections/articles/points/delete":
			_, _ = io.WriteString(w, `{"result":{"operation_id":2,"status":"completed"},"status":"ok"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/collections/articles/points/search":
			_, _ = io.WriteString(w, `{"result":[{"id":"a1","score":0.99,"payload":{"title":"hello"}}],"status":"ok"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"status":"not_found"}`)
		}
	}))
	t.Cleanup(server.Close)
	return server, log
}

func qdrantRequests(log *qdrantLog) []qdrantRequest {
	log.mu.Lock()
	defer log.mu.Unlock()
	requests := make([]qdrantRequest, len(log.requests))
	copy(requests, log.requests)
	return requests
}

func hasQdrantRequest(requests []qdrantRequest, method string, path string) bool {
	return findQdrantRequest(requests, method, path).Path != ""
}

func findQdrantRequest(requests []qdrantRequest, method string, path string) qdrantRequest {
	for _, request := range requests {
		if request.Method == method && request.Path == path {
			return request
		}
	}
	return qdrantRequest{}
}
