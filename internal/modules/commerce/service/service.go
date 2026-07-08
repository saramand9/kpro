// File: internal/modules/commerce/service/service.go
// Purpose: Provides commerce business operations over a GORM-backed store.

package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/saramand9/bignet/kpro/internal/modules/commerce"
)

var ErrInvalidInput = commerce.ErrInvalidInput

type Service struct {
	store Store
}

func NewWithStore(store Store) *Service {
	return &Service{store: store}
}

func (s *Service) CreateProduct(ctx context.Context, input CreateProductInput) (commerce.Product, error) {
	input.normalize()
	if err := input.validate(); err != nil {
		return commerce.Product{}, err
	}
	return s.store.CreateProduct(ctx, commerce.Product{
		SKU:        input.SKU,
		Name:       input.Name,
		PriceCents: input.PriceCents,
		Stock:      input.Stock,
	})
}

func (s *Service) ListProducts(ctx context.Context) ([]commerce.Product, error) {
	return s.store.ListProducts(ctx)
}

func (s *Service) CreateOrder(ctx context.Context, input CreateOrderInput) (commerce.Order, error) {
	input.BuyerName = strings.TrimSpace(input.BuyerName)
	if input.BuyerName == "" {
		return commerce.Order{}, fmt.Errorf("%w: buyer_name is required", ErrInvalidInput)
	}
	if len(input.Items) == 0 {
		return commerce.Order{}, fmt.Errorf("%w: at least one item is required", ErrInvalidInput)
	}
	seen := make(map[int64]struct{}, len(input.Items))
	for i, item := range input.Items {
		if item.ProductID <= 0 {
			return commerce.Order{}, fmt.Errorf("%w: item %d product_id is required", ErrInvalidInput, i)
		}
		if item.Quantity <= 0 {
			return commerce.Order{}, fmt.Errorf("%w: item %d quantity must be positive", ErrInvalidInput, i)
		}
		if _, ok := seen[item.ProductID]; ok {
			return commerce.Order{}, fmt.Errorf("%w: duplicate product_id %d", ErrInvalidInput, item.ProductID)
		}
		seen[item.ProductID] = struct{}{}
	}
	return s.store.CreateOrder(ctx, input)
}

func (s *Service) FindOrder(ctx context.Context, id int64) (commerce.Order, error) {
	if id <= 0 {
		return commerce.Order{}, fmt.Errorf("%w: id must be positive", ErrInvalidInput)
	}
	return s.store.FindOrder(ctx, id)
}

func (s *Service) SalesSummary(ctx context.Context) (commerce.SalesSummary, error) {
	return s.store.SalesSummary(ctx)
}

func (i *CreateProductInput) normalize() {
	i.SKU = strings.ToUpper(strings.TrimSpace(i.SKU))
	i.Name = strings.TrimSpace(i.Name)
}

func (i CreateProductInput) validate() error {
	if i.SKU == "" {
		return fmt.Errorf("%w: sku is required", ErrInvalidInput)
	}
	if i.Name == "" {
		return fmt.Errorf("%w: name is required", ErrInvalidInput)
	}
	if i.PriceCents <= 0 {
		return fmt.Errorf("%w: price_cents must be positive", ErrInvalidInput)
	}
	if i.Stock < 0 {
		return fmt.Errorf("%w: stock must be non-negative", ErrInvalidInput)
	}
	return nil
}

func PlacedStatus() string {
	return commerce.OrderStatusPlaced
}
