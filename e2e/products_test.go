package e2e

import (
	"database/sql"
	"fmt"
	"net/http"
	"testing"

	_ "github.com/go-sql-driver/mysql"
)

// TestProductCRUDAcrossEngines runs the product lifecycle over HTTP (quark on
// PostgreSQL) and asserts the audit trail lands on the aliased MySQL database
// (quark on MySQL) — both through the API and directly against MySQL — plus
// the audit module bound to the "audit" alias via the framework's managed
// handle.
func TestProductCRUDAcrossEngines(t *testing.T) {
	base := appURL(t)
	c := client(t)

	// Mutations require a session.
	if status := doJSON(t, c, http.MethodPost, base+"/api/products", map[string]any{
		"sku": "nope", "name": "x", "price_cents": 1, "stock": 1,
	}, nil); status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated create: status %d (want 401)", status)
	}

	login(t, c, base)

	sku := uniqueSKU("crud")
	id := createProduct(t, c, base, sku, 7)

	// Read back.
	var got struct {
		ID    int64  `json:"id"`
		SKU   string `json:"sku"`
		Name  string `json:"name"`
		Stock int64  `json:"stock"`
	}
	if status := doJSON(t, c, http.MethodGet, fmt.Sprintf("%s/api/products/%d", base, id), nil, &got); status != http.StatusOK {
		t.Fatalf("get product: status %d", status)
	}
	if got.SKU != sku || got.Stock != 7 {
		t.Fatalf("get product: %+v", got)
	}

	// Update.
	if status := doJSON(t, c, http.MethodPut, fmt.Sprintf("%s/api/products/%d", base, id), map[string]any{
		"name": "Renamed " + sku, "price_cents": 999,
	}, &got); status != http.StatusOK {
		t.Fatalf("update product: status %d", status)
	}
	if got.Name != "Renamed "+sku {
		t.Fatalf("update product: name %q", got.Name)
	}

	// List contains it.
	var list struct {
		Products []struct {
			ID int64 `json:"id"`
		} `json:"products"`
	}
	if status := doJSON(t, c, http.MethodGet, base+"/api/products", nil, &list); status != http.StatusOK {
		t.Fatalf("list products: status %d", status)
	}
	found := false
	for _, p := range list.Products {
		if p.ID == id {
			found = true
		}
	}
	if !found {
		t.Fatalf("product %d missing from list", id)
	}

	// Audit trail via the API (quark reading MySQL).
	var movements struct {
		Movements []struct {
			ProductID int64  `json:"product_id"`
			Delta     int64  `json:"delta"`
			Reason    string `json:"reason"`
		} `json:"movements"`
	}
	if status := doJSON(t, c, http.MethodGet,
		fmt.Sprintf("%s/api/stock-movements?product_id=%d", base, id), nil, &movements); status != http.StatusOK {
		t.Fatalf("list stock movements: status %d", status)
	}
	if len(movements.Movements) == 0 || movements.Movements[len(movements.Movements)-1].Reason != "initial" {
		t.Fatalf("expected an 'initial' stock movement on MySQL, got %+v", movements.Movements)
	}

	// Audit trail directly against MySQL: the row is really there.
	mysqlDSN := envOr("QA_MYSQL_DSN", "warehouse:warehouse@tcp(127.0.0.1:53306)/warehouse_audit?parseTime=true")
	mdb, err := sql.Open("mysql", mysqlDSN)
	if err != nil {
		t.Fatalf("open mysql: %v", err)
	}
	defer mdb.Close()
	var n int
	if err := mdb.QueryRow(
		"SELECT COUNT(*) FROM stock_movements WHERE product_id = ? AND reason = 'initial'", id).Scan(&n); err != nil {
		t.Fatalf("mysql count: %v", err)
	}
	if n != 1 {
		t.Fatalf("mysql stock_movements rows for product %d = %d (want 1)", id, n)
	}

	// The audit module runs on the framework-managed handle for the "audit"
	// alias (databases.audit in the config).
	var health struct {
		Database       string `json:"database"`
		Engine         string `json:"engine"`
		StockMovements int64  `json:"stock_movements"`
	}
	if status := doJSON(t, c, http.MethodGet, base+"/api/audit/health", nil, &health); status != http.StatusOK {
		t.Fatalf("audit health: status %d", status)
	}
	if health.Database != "audit" || health.StockMovements < 1 {
		t.Fatalf("audit health: %+v", health)
	}

	// Delete.
	if status := doJSON(t, c, http.MethodDelete, fmt.Sprintf("%s/api/products/%d", base, id), nil, nil); status != http.StatusNoContent {
		t.Fatalf("delete product: status %d", status)
	}
	if status := doJSON(t, c, http.MethodGet, fmt.Sprintf("%s/api/products/%d", base, id), nil, nil); status != http.StatusNotFound {
		t.Fatalf("get deleted product: status %d (want 404)", status)
	}
}
