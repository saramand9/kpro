// File: internal/modules/commerce/store/store.go
// Purpose: Adapts GORM to commerce service persistence operations.

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/saramand9/bignet/kpro/database/common/lifecycle"
	gormstore "github.com/saramand9/bignet/kpro/database/gorm"
	"github.com/saramand9/bignet/kpro/internal/modules/commerce"
	"gorm.io/gorm"
)

// Store adapts GORM to commerce business semantics.
type Store struct {
	db *gorm.DB
}

type pendingOrderItem struct {
	product productRow
	input   commerce.CreateOrderItemInput
}

// NewGORM creates a commerce store backed by GORM.
func NewGORM(database *gormstore.Database) *Store {
	if database == nil {
		return &Store{}
	}
	return &Store{db: database.GORM()}
}

// CreateProduct inserts a product and returns the persisted row.
func (s *Store) CreateProduct(ctx context.Context, product commerce.Product) (commerce.Product, error) {
	db, err := s.gormDB()
	if err != nil {
		return commerce.Product{}, err
	}
	row := productRowFromDomain(product)
	if err := db.WithContext(ctx).Create(&row).Error; err != nil {
		return commerce.Product{}, err
	}
	return productRowToDomain(row), nil
}

// ListProducts returns all products ordered by id.
func (s *Store) ListProducts(ctx context.Context) ([]commerce.Product, error) {
	db, err := s.gormDB()
	if err != nil {
		return nil, err
	}
	var rows []productRow
	if err := db.WithContext(ctx).Order("id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return productRowsToDomain(rows), nil
}

// CreateOrder creates an order and decrements stock in one database transaction.
func (s *Store) CreateOrder(ctx context.Context, input commerce.CreateOrderInput) (commerce.Order, error) {
	db, err := s.gormDB()
	if err != nil {
		return commerce.Order{}, err
	}
	var orderID int64
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		pending := make([]pendingOrderItem, 0, len(input.Items))
		totalCents := int64(0)
		for _, item := range input.Items {
			product, err := findProductForOrder(tx, item.ProductID)
			if err != nil {
				return err
			}
			if product.Stock < item.Quantity {
				return fmt.Errorf("%w: product %d stock is insufficient", commerce.ErrInvalidInput, item.ProductID)
			}
			if err := decrementProductStock(tx, product.ID, item.Quantity); err != nil {
				return err
			}
			totalCents += product.PriceCents * int64(item.Quantity)
			pending = append(pending, pendingOrderItem{product: product, input: item})
		}
		order := orderRow{
			BuyerName:  input.BuyerName,
			Status:     commerce.OrderStatusPlaced,
			TotalCents: totalCents,
		}
		if err := tx.Create(&order).Error; err != nil {
			return err
		}
		items := make([]orderItemRow, 0, len(pending))
		for _, value := range pending {
			items = append(items, orderItemRow{
				OrderID:        order.ID,
				ProductID:      value.product.ID,
				ProductSKU:     value.product.SKU,
				ProductName:    value.product.Name,
				Quantity:       value.input.Quantity,
				UnitPriceCents: value.product.PriceCents,
				LineTotalCents: value.product.PriceCents * int64(value.input.Quantity),
			})
		}
		if err := tx.Create(&items).Error; err != nil {
			return err
		}
		orderID = order.ID
		return nil
	})
	if err != nil {
		return commerce.Order{}, err
	}
	return s.FindOrder(ctx, orderID)
}

// FindOrder returns one order with its items preloaded.
func (s *Store) FindOrder(ctx context.Context, id int64) (commerce.Order, error) {
	db, err := s.gormDB()
	if err != nil {
		return commerce.Order{}, err
	}
	var row orderRow
	err = db.WithContext(ctx).
		Preload("Items", func(db *gorm.DB) *gorm.DB {
			return db.Order("id ASC")
		}).
		First(&row, id).Error
	if err != nil {
		return commerce.Order{}, normalizeFindError(err)
	}
	return orderRowToDomain(row), nil
}

// SalesSummary runs a raw aggregate query over orders and order items.
func (s *Store) SalesSummary(ctx context.Context) (commerce.SalesSummary, error) {
	db, err := s.gormDB()
	if err != nil {
		return commerce.SalesSummary{}, err
	}
	var summary commerce.SalesSummary
	err = db.WithContext(ctx).Raw(`
SELECT
	COUNT(DISTINCT o.id) AS order_count,
	COALESCE(SUM(i.quantity), 0) AS item_count,
	COALESCE(SUM(i.line_total_cents), 0) AS gross_revenue_cents
FROM commerce_orders o
LEFT JOIN commerce_order_items i ON i.order_id = o.id
`).Scan(&summary).Error
	if err != nil {
		return commerce.SalesSummary{}, err
	}
	return summary, nil
}

func findProductForOrder(tx *gorm.DB, productID int64) (productRow, error) {
	var product productRow
	if err := tx.First(&product, productID).Error; err != nil {
		return productRow{}, normalizeFindError(err)
	}
	return product, nil
}

func decrementProductStock(tx *gorm.DB, productID int64, quantity int) error {
	result := tx.Model(&productRow{}).
		Where("id = ? AND stock >= ?", productID, quantity).
		Update("stock", gorm.Expr("stock - ?", quantity))
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("%w: product %d stock is insufficient", commerce.ErrInvalidInput, productID)
	}
	return nil
}

func (s *Store) gormDB() (*gorm.DB, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("%w: commerce gorm store is nil", lifecycle.ErrUnavailable)
	}
	return s.db, nil
}

func normalizeFindError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return sql.ErrNoRows
	}
	return err
}
