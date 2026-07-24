// File: internal/modules/commerce/service/dto.go
// Purpose: Defines commerce service input DTOs.

package service

import "github.com/saramand9/kpro/internal/modules/commerce"

type CreateProductInput struct {
	SKU        string
	Name       string
	PriceCents int64
	Stock      int
}

type CreateOrderInput = commerce.CreateOrderInput
type CreateOrderItemInput = commerce.CreateOrderItemInput
