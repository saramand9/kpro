// File: cmd/app/database_test.go
// Purpose: Verifies application GORM persistence wiring.

package main

import (
	"context"
	"testing"

	"github.com/saramand9/bignet/kpro/internal/app/persistence"
	commerceservice "github.com/saramand9/bignet/kpro/internal/modules/commerce/service"
	projectservice "github.com/saramand9/bignet/kpro/internal/modules/projects/service"
	userservice "github.com/saramand9/bignet/kpro/internal/modules/users/service"
)

func TestInitAppPersistenceCreatesServices(t *testing.T) {
	resources, services, err := initAppPersistence(context.Background(), persistence.Config{
		SQL: persistence.SQLConfig{Driver: "sqlite", DSN: ":memory:"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resources == nil || resources.SQL == nil || services.Users == nil || services.Projects == nil || services.Commerce == nil {
		t.Fatalf("resources=%v services=%#v", resources, services)
	}
	defer func() {
		if err := resources.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
	}()

	createdUser, err := services.Users.Create(context.Background(), userservice.CreateInput{Name: "Ada", Age: 37})
	if err != nil {
		t.Fatal(err)
	}
	if createdUser.ID == 0 || createdUser.Name != "Ada" || createdUser.Age != 37 || createdUser.CreatedAt.IsZero() {
		t.Fatalf("created user = %#v", createdUser)
	}

	createdProject, err := services.Projects.Create(context.Background(), projectservice.CreateInput{Name: "Apollo"})
	if err != nil {
		t.Fatal(err)
	}
	if createdProject.ID == 0 || createdProject.Name != "Apollo" || createdProject.Status != "active" || createdProject.CreatedAt.IsZero() {
		t.Fatalf("created project = %#v", createdProject)
	}

	createdProduct, err := services.Commerce.CreateProduct(context.Background(), commerceservice.CreateProductInput{
		SKU:        "key-001",
		Name:       "Keyboard",
		PriceCents: 1200,
		Stock:      5,
	})
	if err != nil {
		t.Fatal(err)
	}
	createdOrder, err := services.Commerce.CreateOrder(context.Background(), commerceservice.CreateOrderInput{
		BuyerName: "Ada",
		Items: []commerceservice.CreateOrderItemInput{
			{ProductID: createdProduct.ID, Quantity: 2},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if createdOrder.ID == 0 || createdOrder.TotalCents != 2400 || len(createdOrder.Items) != 1 {
		t.Fatalf("created order = %#v", createdOrder)
	}
}

func TestInitAppPersistenceDisabled(t *testing.T) {
	resources, services, err := initAppPersistence(context.Background(), persistence.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if resources == nil || resources.Managed() || services.Users != nil || services.Projects != nil || services.Commerce != nil {
		t.Fatalf("resources=%v services=%#v", resources, services)
	}
}
