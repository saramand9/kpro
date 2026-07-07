package mongostore

import (
	"context"
	"errors"
	"testing"

	"github.com/saramand9/bignet/kpro/database/kernel"
)

type mongoUser struct {
	ID   string `bson:"_id"`
	Name string `bson:"name"`
}

func TestOpenValidatesConfig(t *testing.T) {
	if _, err := Open(context.Background(), Config{}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("missing config err=%v, want ErrInvalidConfig", err)
	}
	if _, err := Open(context.Background(), Config{URI: "mongodb://127.0.0.1:27017"}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("missing database err=%v, want ErrInvalidConfig", err)
	}
}

func TestOpenSkipPingCreatesClient(t *testing.T) {
	client, err := Open(context.Background(), Config{
		Name:          "documents",
		URI:           "mongodb://127.0.0.1:1",
		Database:      "app",
		SkipPingCheck: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	if client.Name() != "documents" || client.Kind() != kernel.KindDocument {
		t.Fatalf("resource=%s/%s", client.Name(), client.Kind())
	}
	if client.Mongo() == nil || client.Database() == nil {
		t.Fatal("missing underlying mongo handles")
	}
	if client.DatabaseName() != "app" {
		t.Fatalf("database name=%q, want app", client.DatabaseName())
	}
	collection := Collection[mongoUser](client, "users")
	if collection.Name() != "users" || collection.Mongo() == nil {
		t.Fatalf("collection=%#v", collection)
	}
}

func TestCollectionValidatesState(t *testing.T) {
	_, _, err := NewCollection[mongoUser](nil, "users").FindByID(context.Background(), "1")
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("nil collection err=%v, want ErrInvalidConfig", err)
	}
	client, err := Open(context.Background(), Config{
		URI:           "mongodb://127.0.0.1:1",
		Database:      "app",
		SkipPingCheck: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	_, _, err = Collection[mongoUser](client, "").FindByID(context.Background(), "1")
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("empty collection err=%v, want ErrInvalidRequest", err)
	}
}
