// File: internal/modules/commerce/handler/http.go
// Purpose: Provides HTTP DTO handlers for commerce GORM workflows.

package handler

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/saramand9/bignet/kpro/internal/modules/commerce"
	commerceservice "github.com/saramand9/bignet/kpro/internal/modules/commerce/service"
	"github.com/saramand9/bignet/kpro/transport/httpx"
)

// CreateProductRequest is the public HTTP input for creating a product.
type CreateProductRequest struct {
	SKU        string `json:"sku"`
	Name       string `json:"name"`
	PriceCents int64  `json:"price_cents"`
	Stock      int    `json:"stock"`
}

func (r CreateProductRequest) toInput() commerceservice.CreateProductInput {
	return commerceservice.CreateProductInput{
		SKU:        r.SKU,
		Name:       r.Name,
		PriceCents: r.PriceCents,
		Stock:      r.Stock,
	}
}

// CreateOrderRequest is the public HTTP input for creating an order.
type CreateOrderRequest struct {
	BuyerName string                   `json:"buyer_name"`
	Items     []CreateOrderItemRequest `json:"items"`
}

func (r CreateOrderRequest) toInput() commerceservice.CreateOrderInput {
	items := make([]commerceservice.CreateOrderItemInput, 0, len(r.Items))
	for _, item := range r.Items {
		items = append(items, commerceservice.CreateOrderItemInput{
			ProductID: item.ProductID,
			Quantity:  item.Quantity,
		})
	}
	return commerceservice.CreateOrderInput{
		BuyerName: r.BuyerName,
		Items:     items,
	}
}

// CreateOrderItemRequest is one product line in an order request.
type CreateOrderItemRequest struct {
	ProductID int64 `json:"product_id"`
	Quantity  int   `json:"quantity"`
}

type productResponse struct {
	ID         int64     `json:"id"`
	SKU        string    `json:"sku"`
	Name       string    `json:"name"`
	PriceCents int64     `json:"price_cents"`
	Stock      int       `json:"stock"`
	CreatedAt  time.Time `json:"created_at"`
}

type orderResponse struct {
	ID         int64               `json:"id"`
	BuyerName  string              `json:"buyer_name"`
	Status     string              `json:"status"`
	TotalCents int64               `json:"total_cents"`
	Items      []orderItemResponse `json:"items"`
	CreatedAt  time.Time           `json:"created_at"`
}

type orderItemResponse struct {
	ID             int64  `json:"id"`
	OrderID        int64  `json:"order_id"`
	ProductID      int64  `json:"product_id"`
	ProductSKU     string `json:"product_sku"`
	ProductName    string `json:"product_name"`
	Quantity       int    `json:"quantity"`
	UnitPriceCents int64  `json:"unit_price_cents"`
	LineTotalCents int64  `json:"line_total_cents"`
}

type salesSummaryResponse struct {
	OrderCount        int64 `json:"order_count"`
	ItemCount         int64 `json:"item_count"`
	GrossRevenueCents int64 `json:"gross_revenue_cents"`
}

// RegisterHTTP exposes commerce routes with HTTP DTO handlers.
func RegisterHTTP(endpoint *httpx.Endpoint, service *commerceservice.Service) {
	endpoint.Get("/commerce/products", getProducts(service))
	endpoint.Post("/commerce/products", createProduct(service))
	endpoint.Post("/commerce/orders", createOrder(service))
	endpoint.Get("/commerce/orders", getOrder(service))
	endpoint.Get("/commerce/sales-summary", getSalesSummary(service))
}

func getProducts(service *commerceservice.Service) httpx.Handler {
	return func(ctx *httpx.Context) error {
		products, err := service.ListProducts(ctx.Context())
		if err != nil {
			return err
		}
		return ctx.JSON(http.StatusOK, newProductResponses(products))
	}
}

func createProduct(service *commerceservice.Service) httpx.Handler {
	return func(ctx *httpx.Context) error {
		var request CreateProductRequest
		if err := ctx.JSONBody(&request); err != nil {
			return writeError(ctx, http.StatusBadRequest, "invalid json")
		}
		product, err := service.CreateProduct(ctx.Context(), request.toInput())
		if err != nil {
			return writeServiceError(ctx, err)
		}
		return ctx.JSON(http.StatusCreated, newProductResponse(product))
	}
}

func createOrder(service *commerceservice.Service) httpx.Handler {
	return func(ctx *httpx.Context) error {
		var request CreateOrderRequest
		if err := ctx.JSONBody(&request); err != nil {
			return writeError(ctx, http.StatusBadRequest, "invalid json")
		}
		order, err := service.CreateOrder(ctx.Context(), request.toInput())
		if err != nil {
			return writeServiceError(ctx, err)
		}
		return ctx.JSON(http.StatusCreated, newOrderResponse(order))
	}
}

func getOrder(service *commerceservice.Service) httpx.Handler {
	return func(ctx *httpx.Context) error {
		orderID, err := parseID(ctx.Query("id"))
		if err != nil {
			return writeError(ctx, http.StatusBadRequest, err.Error())
		}
		order, err := service.FindOrder(ctx.Context(), orderID)
		if err != nil {
			return writeServiceError(ctx, err)
		}
		return ctx.JSON(http.StatusOK, newOrderResponse(order))
	}
}

func getSalesSummary(service *commerceservice.Service) httpx.Handler {
	return func(ctx *httpx.Context) error {
		summary, err := service.SalesSummary(ctx.Context())
		if err != nil {
			return err
		}
		return ctx.JSON(http.StatusOK, newSalesSummaryResponse(summary))
	}
}

func parseID(raw string) (int64, error) {
	if strings.TrimSpace(raw) == "" {
		return 0, fmt.Errorf("id is required")
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("id must be a positive integer")
	}
	return id, nil
}

func writeServiceError(ctx *httpx.Context, err error) error {
	if errors.Is(err, commerceservice.ErrInvalidInput) {
		return writeError(ctx, http.StatusBadRequest, err.Error())
	}
	if errors.Is(err, sql.ErrNoRows) {
		return writeError(ctx, http.StatusNotFound, "commerce record not found")
	}
	return err
}

func writeError(ctx *httpx.Context, status int, message string) error {
	return ctx.JSON(status, map[string]string{"error": message})
}

func newProductResponses(values []commerce.Product) []productResponse {
	responses := make([]productResponse, 0, len(values))
	for _, value := range values {
		responses = append(responses, newProductResponse(value))
	}
	return responses
}

func newProductResponse(value commerce.Product) productResponse {
	return productResponse{
		ID:         value.ID,
		SKU:        value.SKU,
		Name:       value.Name,
		PriceCents: value.PriceCents,
		Stock:      value.Stock,
		CreatedAt:  value.CreatedAt,
	}
}

func newOrderResponse(value commerce.Order) orderResponse {
	return orderResponse{
		ID:         value.ID,
		BuyerName:  value.BuyerName,
		Status:     value.Status,
		TotalCents: value.TotalCents,
		Items:      newOrderItemResponses(value.Items),
		CreatedAt:  value.CreatedAt,
	}
}

func newOrderItemResponses(values []commerce.OrderItem) []orderItemResponse {
	responses := make([]orderItemResponse, 0, len(values))
	for _, value := range values {
		responses = append(responses, newOrderItemResponse(value))
	}
	return responses
}

func newOrderItemResponse(value commerce.OrderItem) orderItemResponse {
	return orderItemResponse{
		ID:             value.ID,
		OrderID:        value.OrderID,
		ProductID:      value.ProductID,
		ProductSKU:     value.ProductSKU,
		ProductName:    value.ProductName,
		Quantity:       value.Quantity,
		UnitPriceCents: value.UnitPriceCents,
		LineTotalCents: value.LineTotalCents,
	}
}

func newSalesSummaryResponse(value commerce.SalesSummary) salesSummaryResponse {
	return salesSummaryResponse{
		OrderCount:        value.OrderCount,
		ItemCount:         value.ItemCount,
		GrossRevenueCents: value.GrossRevenueCents,
	}
}
