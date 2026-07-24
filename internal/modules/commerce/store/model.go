// File: internal/modules/commerce/store/model.go
// Purpose: Defines GORM persistence rows and schema migrations for commerce.

package store

import (
	"context"
	"time"

	gormstore "github.com/saramand9/kpro/database/gorm"
	"github.com/saramand9/kpro/internal/modules/commerce"
)

type productRow struct {
	ID         int64     `gorm:"column:id;primaryKey;autoIncrement"`
	SKU        string    `gorm:"column:sku;not null;uniqueIndex"`
	Name       string    `gorm:"column:name;not null"`
	PriceCents int64     `gorm:"column:price_cents;not null"`
	Stock      int       `gorm:"column:stock;not null"`
	CreatedAt  time.Time `gorm:"column:created_at;autoCreateTime"`
}

func (productRow) TableName() string {
	return commerce.ProductsTable
}

type orderRow struct {
	ID         int64          `gorm:"column:id;primaryKey;autoIncrement"`
	BuyerName  string         `gorm:"column:buyer_name;not null"`
	Status     string         `gorm:"column:status;not null"`
	TotalCents int64          `gorm:"column:total_cents;not null"`
	Items      []orderItemRow `gorm:"foreignKey:OrderID;references:ID"`
	CreatedAt  time.Time      `gorm:"column:created_at;autoCreateTime"`
}

func (orderRow) TableName() string {
	return commerce.OrdersTable
}

type orderItemRow struct {
	ID             int64  `gorm:"column:id;primaryKey;autoIncrement"`
	OrderID        int64  `gorm:"column:order_id;not null;index"`
	ProductID      int64  `gorm:"column:product_id;not null;index"`
	ProductSKU     string `gorm:"column:product_sku;not null"`
	ProductName    string `gorm:"column:product_name;not null"`
	Quantity       int    `gorm:"column:quantity;not null"`
	UnitPriceCents int64  `gorm:"column:unit_price_cents;not null"`
	LineTotalCents int64  `gorm:"column:line_total_cents;not null"`
}

func (orderItemRow) TableName() string {
	return commerce.OrderItemsTable
}

// InitGORM applies commerce module schema migrations.
func InitGORM(ctx context.Context, database *gormstore.Database) error {
	return gormstore.ApplyMigrations(ctx, database, Migrations())
}

// Migrations returns versioned commerce schema migrations.
func Migrations() []gormstore.Migration {
	return []gormstore.Migration{
		{
			Version: 202607080301,
			Name:    "create_commerce_products",
			Statements: []gormstore.Statement{
				gormstore.DialectSQL(
					`CREATE TABLE IF NOT EXISTS commerce_products (
id BIGSERIAL PRIMARY KEY,
sku VARCHAR(64) NOT NULL UNIQUE,
name TEXT NOT NULL,
price_cents BIGINT NOT NULL,
stock INTEGER NOT NULL,
created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
)`,
					map[string]string{
						"sqlite": `CREATE TABLE IF NOT EXISTS commerce_products (
id INTEGER PRIMARY KEY AUTOINCREMENT,
sku TEXT NOT NULL UNIQUE,
name TEXT NOT NULL,
price_cents INTEGER NOT NULL,
stock INTEGER NOT NULL,
created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
)`,
						"mysql": `CREATE TABLE IF NOT EXISTS commerce_products (
id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
sku VARCHAR(64) NOT NULL UNIQUE,
name VARCHAR(255) NOT NULL,
price_cents BIGINT NOT NULL,
stock INT NOT NULL,
created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
)`,
					},
				),
			},
		},
		{
			Version: 202607080302,
			Name:    "create_commerce_orders",
			Statements: []gormstore.Statement{
				gormstore.DialectSQL(
					`CREATE TABLE IF NOT EXISTS commerce_orders (
id BIGSERIAL PRIMARY KEY,
buyer_name TEXT NOT NULL,
status VARCHAR(32) NOT NULL,
total_cents BIGINT NOT NULL,
created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
)`,
					map[string]string{
						"sqlite": `CREATE TABLE IF NOT EXISTS commerce_orders (
id INTEGER PRIMARY KEY AUTOINCREMENT,
buyer_name TEXT NOT NULL,
status TEXT NOT NULL,
total_cents INTEGER NOT NULL,
created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
)`,
						"mysql": `CREATE TABLE IF NOT EXISTS commerce_orders (
id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
buyer_name VARCHAR(255) NOT NULL,
status VARCHAR(32) NOT NULL,
total_cents BIGINT NOT NULL,
created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
)`,
					},
				),
			},
		},
		{
			Version: 202607080303,
			Name:    "create_commerce_order_items",
			Statements: []gormstore.Statement{
				gormstore.DialectSQL(
					`CREATE TABLE IF NOT EXISTS commerce_order_items (
id BIGSERIAL PRIMARY KEY,
order_id BIGINT NOT NULL,
product_id BIGINT NOT NULL,
product_sku VARCHAR(64) NOT NULL,
product_name TEXT NOT NULL,
quantity INTEGER NOT NULL,
unit_price_cents BIGINT NOT NULL,
line_total_cents BIGINT NOT NULL
)`,
					map[string]string{
						"sqlite": `CREATE TABLE IF NOT EXISTS commerce_order_items (
id INTEGER PRIMARY KEY AUTOINCREMENT,
order_id INTEGER NOT NULL,
product_id INTEGER NOT NULL,
product_sku TEXT NOT NULL,
product_name TEXT NOT NULL,
quantity INTEGER NOT NULL,
unit_price_cents INTEGER NOT NULL,
line_total_cents INTEGER NOT NULL
)`,
						"mysql": `CREATE TABLE IF NOT EXISTS commerce_order_items (
id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
order_id BIGINT NOT NULL,
product_id BIGINT NOT NULL,
product_sku VARCHAR(64) NOT NULL,
product_name VARCHAR(255) NOT NULL,
quantity INT NOT NULL,
unit_price_cents BIGINT NOT NULL,
line_total_cents BIGINT NOT NULL
)`,
					},
				),
				gormstore.DialectSQL(
					`CREATE INDEX IF NOT EXISTS idx_commerce_order_items_order_id ON commerce_order_items (order_id)`,
					map[string]string{
						"mysql": `CREATE INDEX idx_commerce_order_items_order_id ON commerce_order_items (order_id)`,
					},
				),
				gormstore.DialectSQL(
					`CREATE INDEX IF NOT EXISTS idx_commerce_order_items_product_id ON commerce_order_items (product_id)`,
					map[string]string{
						"mysql": `CREATE INDEX idx_commerce_order_items_product_id ON commerce_order_items (product_id)`,
					},
				),
			},
		},
	}
}

func productRowsToDomain(rows []productRow) []commerce.Product {
	products := make([]commerce.Product, 0, len(rows))
	for _, row := range rows {
		products = append(products, productRowToDomain(row))
	}
	return products
}

func productRowToDomain(row productRow) commerce.Product {
	return commerce.Product{
		ID:         row.ID,
		SKU:        row.SKU,
		Name:       row.Name,
		PriceCents: row.PriceCents,
		Stock:      row.Stock,
		CreatedAt:  row.CreatedAt,
	}
}

func productRowFromDomain(product commerce.Product) productRow {
	return productRow{
		ID:         product.ID,
		SKU:        product.SKU,
		Name:       product.Name,
		PriceCents: product.PriceCents,
		Stock:      product.Stock,
		CreatedAt:  product.CreatedAt,
	}
}

func orderRowToDomain(row orderRow) commerce.Order {
	return commerce.Order{
		ID:         row.ID,
		BuyerName:  row.BuyerName,
		Status:     row.Status,
		TotalCents: row.TotalCents,
		Items:      orderItemRowsToDomain(row.Items),
		CreatedAt:  row.CreatedAt,
	}
}

func orderItemRowsToDomain(rows []orderItemRow) []commerce.OrderItem {
	items := make([]commerce.OrderItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, orderItemRowToDomain(row))
	}
	return items
}

func orderItemRowToDomain(row orderItemRow) commerce.OrderItem {
	return commerce.OrderItem{
		ID:             row.ID,
		OrderID:        row.OrderID,
		ProductID:      row.ProductID,
		ProductSKU:     row.ProductSKU,
		ProductName:    row.ProductName,
		Quantity:       row.Quantity,
		UnitPriceCents: row.UnitPriceCents,
		LineTotalCents: row.LineTotalCents,
	}
}
