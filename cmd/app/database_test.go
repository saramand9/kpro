// File: cmd/app/database_test.go
// Purpose: Verifies application GORM persistence wiring.

package main

import (
	"context"
	"testing"

	userservice "github.com/saramand9/bignet/kpro/internal/modules/users/service"
)

func TestInitAppPersistenceCreatesUserService(t *testing.T) {
	resources, service, err := initAppPersistence(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if resources == nil || resources.SQL == nil || service == nil {
		t.Fatalf("resources=%v service=%v", resources, service)
	}
	defer func() {
		if err := resources.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
	}()

	created, err := service.Create(context.Background(), userservice.CreateInput{Name: "Ada", Age: 37})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == 0 || created.Name != "Ada" || created.Age != 37 || created.CreatedAt.IsZero() {
		t.Fatalf("created = %#v", created)
	}
}

func TestInitAppPersistenceDisabled(t *testing.T) {
	resources, service, err := initAppPersistence(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if resources == nil || resources.Managed() || service != nil {
		t.Fatalf("resources=%v service=%v", resources, service)
	}
}
