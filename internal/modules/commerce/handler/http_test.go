// File: internal/modules/commerce/handler/http_test.go
// Purpose: Tests HTTP commerce routes backed by GORM.

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	gormstore "github.com/saramand9/bignet/kpro/database/gorm"
	commerceservice "github.com/saramand9/bignet/kpro/internal/modules/commerce/service"
	commercestore "github.com/saramand9/bignet/kpro/internal/modules/commerce/store"
	"github.com/saramand9/bignet/kpro/transport/httpx"
	"gorm.io/driver/sqlite"

	_ "modernc.org/sqlite"
)

func TestHTTPCommerceGORMWorkflows(t *testing.T) {
	database := openHTTPTestDatabase(t)
	defer closeHTTPTestDatabase(t, database)

	endpoint := httpx.New(httpx.Config{})
	RegisterHTTP(endpoint, commerceservice.NewWithStore(commercestore.NewGORM(database)))

	invalid := executeHTTPTestRequest(endpoint, http.MethodPost, "/commerce/products", `{"name":"Keyboard","price_cents":1200,"stock":5}`)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid product status = %d, body = %q", invalid.Code, invalid.Body.String())
	}

	keyboard := createProductViaHTTP(t, endpoint, `{"sku":" key-001 ","name":" Keyboard ","price_cents":1200,"stock":5}`)
	if keyboard.ID == 0 || keyboard.SKU != "KEY-001" || keyboard.Name != "Keyboard" {
		t.Fatalf("keyboard = %#v", keyboard)
	}
	mouse := createProductViaHTTP(t, endpoint, `{"sku":"mou-001","name":"Mouse","price_cents":900,"stock":8}`)

	orderBody := `{"buyer_name":"Ada","items":[{"product_id":` + formatID(keyboard.ID) + `,"quantity":2},{"product_id":` + formatID(mouse.ID) + `,"quantity":1}]}`
	orderResp := executeHTTPTestRequest(endpoint, http.MethodPost, "/commerce/orders", orderBody)
	if orderResp.Code != http.StatusCreated {
		t.Fatalf("create order status = %d, body = %q", orderResp.Code, orderResp.Body.String())
	}
	var order orderResponse
	if err := json.Unmarshal(orderResp.Body.Bytes(), &order); err != nil {
		t.Fatal(err)
	}
	if order.ID == 0 || order.TotalCents != 3300 || len(order.Items) != 2 {
		t.Fatalf("order = %#v", order)
	}
	if order.Items[0].ProductSKU != "KEY-001" || order.Items[0].LineTotalCents != 2400 {
		t.Fatalf("first order item = %#v", order.Items[0])
	}

	found := executeHTTPTestRequest(endpoint, http.MethodGet, "/commerce/orders?id="+formatID(order.ID), "")
	if found.Code != http.StatusOK || !strings.Contains(found.Body.String(), `"product_sku":"MOU-001"`) {
		t.Fatalf("find order status/body = %d/%q", found.Code, found.Body.String())
	}

	products := executeHTTPTestRequest(endpoint, http.MethodGet, "/commerce/products", "")
	if products.Code != http.StatusOK || !strings.Contains(products.Body.String(), `"stock":3`) {
		t.Fatalf("list products status/body = %d/%q", products.Code, products.Body.String())
	}

	summaryResp := executeHTTPTestRequest(endpoint, http.MethodGet, "/commerce/sales-summary", "")
	if summaryResp.Code != http.StatusOK {
		t.Fatalf("summary status = %d, body = %q", summaryResp.Code, summaryResp.Body.String())
	}
	var summary salesSummaryResponse
	if err := json.Unmarshal(summaryResp.Body.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.OrderCount != 1 || summary.ItemCount != 3 || summary.GrossRevenueCents != 3300 {
		t.Fatalf("summary = %#v", summary)
	}
}

func createProductViaHTTP(t *testing.T, endpoint *httpx.Endpoint, body string) productResponse {
	t.Helper()
	resp := executeHTTPTestRequest(endpoint, http.MethodPost, "/commerce/products", body)
	if resp.Code != http.StatusCreated {
		t.Fatalf("create product status = %d, body = %q", resp.Code, resp.Body.String())
	}
	var product productResponse
	if err := json.Unmarshal(resp.Body.Bytes(), &product); err != nil {
		t.Fatal(err)
	}
	return product
}

func openHTTPTestDatabase(t *testing.T) *gormstore.Database {
	t.Helper()
	database, err := gormstore.Open(context.Background(), gormstore.Config{
		Dialector:    sqlite.Dialector{DriverName: "sqlite", DSN: ":memory:"},
		MaxOpenConns: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := commercestore.InitGORM(context.Background(), database); err != nil {
		t.Fatal(err)
	}
	return database
}

func closeHTTPTestDatabase(t *testing.T, database *gormstore.Database) {
	t.Helper()
	if err := database.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func executeHTTPTestRequest(endpoint *httpx.Endpoint, method, target, body string) *httptest.ResponseRecorder {
	var reader *bytes.Buffer
	if body == "" {
		reader = bytes.NewBuffer(nil)
	} else {
		reader = bytes.NewBufferString(body)
	}
	req := httptest.NewRequest(method, target, reader)
	recorder := httptest.NewRecorder()
	endpoint.ServeHTTP(recorder, req)
	return recorder
}

func formatID(value int64) string {
	return strconv.FormatInt(value, 10)
}
