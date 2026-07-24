// File: internal/modules/commerce/service/port.go
// Purpose: Defines commerce service persistence ports.

package service

import (
	"context"

	"github.com/saramand9/kpro/internal/modules/commerce"
)

type Store interface {
	CreateProduct(ctx context.Context, product commerce.Product) (commerce.Product, error)
	ListProducts(ctx context.Context) ([]commerce.Product, error)
	CreateOrder(ctx context.Context, input CreateOrderInput) (commerce.Order, error)
	FindOrder(ctx context.Context, id int64) (commerce.Order, error)
	SalesSummary(ctx context.Context) (commerce.SalesSummary, error)
}
