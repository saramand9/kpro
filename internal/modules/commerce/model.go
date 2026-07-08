// File: internal/modules/commerce/model.go
// Purpose: Defines commerce domain models and commands used by GORM flows.

package commerce

import (
	"errors"
	"time"
)

const (
	ProductsTable   = "commerce_products"
	OrdersTable     = "commerce_orders"
	OrderItemsTable = "commerce_order_items"

	OrderStatusPlaced = "placed"
)

var ErrInvalidInput = errors.New("invalid commerce input")

type Product struct {
	ID         int64
	SKU        string
	Name       string
	PriceCents int64
	Stock      int
	CreatedAt  time.Time
}

type Order struct {
	ID         int64
	BuyerName  string
	Status     string
	TotalCents int64
	Items      []OrderItem
	CreatedAt  time.Time
}

type OrderItem struct {
	ID             int64
	OrderID        int64
	ProductID      int64
	ProductSKU     string
	ProductName    string
	Quantity       int
	UnitPriceCents int64
	LineTotalCents int64
}

type CreateOrderInput struct {
	BuyerName string
	Items     []CreateOrderItemInput
}

type CreateOrderItemInput struct {
	ProductID int64
	Quantity  int
}

type SalesSummary struct {
	OrderCount        int64
	ItemCount         int64
	GrossRevenueCents int64
}
