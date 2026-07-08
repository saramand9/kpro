// File: internal/modules/commerce/store/store_test.go
// Purpose: Tests commerce GORM workflows directly at the store layer.

package store

import (
	"context"
	"errors"
	"testing"

	gormstore "github.com/saramand9/bignet/kpro/database/gorm"
	"github.com/saramand9/bignet/kpro/internal/modules/commerce"
	commerceservice "github.com/saramand9/bignet/kpro/internal/modules/commerce/service"
	"gorm.io/driver/sqlite"

	_ "modernc.org/sqlite"
)

func TestGORMStoreCommerceWorkflows(t *testing.T) {
	database := openStoreTestDatabase(t)
	defer closeStoreTestDatabase(t, database)
	store := NewGORM(database)
	ctx := context.Background()

	keyboard, err := store.CreateProduct(ctx, commerce.Product{
		SKU:        "KEY-001",
		Name:       "Keyboard",
		PriceCents: 1200,
		Stock:      5,
	})
	if err != nil {
		t.Fatal(err)
	}
	mouse, err := store.CreateProduct(ctx, commerce.Product{
		SKU:        "MOU-001",
		Name:       "Mouse",
		PriceCents: 900,
		Stock:      8,
	})
	if err != nil {
		t.Fatal(err)
	}

	order, err := store.CreateOrder(ctx, commerceservice.CreateOrderInput{
		BuyerName: "Ada",
		Items: []commerceservice.CreateOrderItemInput{
			{ProductID: keyboard.ID, Quantity: 2},
			{ProductID: mouse.ID, Quantity: 1},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if order.ID == 0 || order.Status != commerceservice.PlacedStatus() || order.TotalCents != 3300 {
		t.Fatalf("order = %#v", order)
	}
	if len(order.Items) != 2 {
		t.Fatalf("order items = %#v", order.Items)
	}
	if order.Items[0].ProductName != "Keyboard" || order.Items[0].LineTotalCents != 2400 {
		t.Fatalf("first order item = %#v", order.Items[0])
	}

	found, err := store.FindOrder(ctx, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(found.Items) != 2 || found.Items[1].ProductSKU != "MOU-001" {
		t.Fatalf("preloaded order = %#v", found)
	}

	products, err := store.ListProducts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if products[0].Stock != 3 || products[1].Stock != 7 {
		t.Fatalf("products after stock decrement = %#v", products)
	}

	summary, err := store.SalesSummary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if summary.OrderCount != 1 || summary.ItemCount != 3 || summary.GrossRevenueCents != 3300 {
		t.Fatalf("summary = %#v", summary)
	}

	_, err = store.CreateOrder(ctx, commerceservice.CreateOrderInput{
		BuyerName: "Grace",
		Items: []commerceservice.CreateOrderItemInput{
			{ProductID: keyboard.ID, Quantity: 99},
		},
	})
	if !errors.Is(err, commerceservice.ErrInvalidInput) {
		t.Fatalf("insufficient stock error = %v", err)
	}
}

func openStoreTestDatabase(t *testing.T) *gormstore.Database {
	t.Helper()
	database, err := gormstore.Open(context.Background(), gormstore.Config{
		Dialector:    sqlite.Dialector{DriverName: "sqlite", DSN: ":memory:"},
		MaxOpenConns: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := InitGORM(context.Background(), database); err != nil {
		t.Fatal(err)
	}
	return database
}

func closeStoreTestDatabase(t *testing.T, database *gormstore.Database) {
	t.Helper()
	if err := database.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}
