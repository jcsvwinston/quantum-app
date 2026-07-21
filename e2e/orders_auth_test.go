package e2e

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

// TestOrdersReadRequireSession asserts that the order read endpoints expose
// customer PII (customer_email) only to an authenticated session: both
// GET /api/orders and GET /api/orders/{id} answer 401 without a session and
// 200 with one. Placing an order (POST /api/orders) stays open — customers
// order without an operator login — but reading orders back is operator-only.
func TestOrdersReadRequireSession(t *testing.T) {
	base := appURL(t)

	// Provision a real order with an authenticated client first, so the
	// with-session half of the test reads real data.
	ops := client(t)
	login(t, ops, base)
	productID := createProduct(t, ops, base, uniqueSKU("ordauth"), 3)
	customer := fmt.Sprintf("pii+%d@example.test", time.Now().UnixNano())
	var placed struct {
		ID int64 `json:"id"`
	}
	if status := doJSON(t, ops, http.MethodPost, base+"/api/orders", map[string]any{
		"product_id": productID, "quantity": 1, "customer_email": customer,
	}, &placed); status != http.StatusCreated {
		t.Fatalf("create order: status %d", status)
	}

	// Without a session: both reads are 401 and leak nothing.
	anon := client(t)
	if status := doJSON(t, anon, http.MethodGet, base+"/api/orders", nil, nil); status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated GET /api/orders: status %d (want 401)", status)
	}
	if status := doJSON(t, anon, http.MethodGet,
		fmt.Sprintf("%s/api/orders/%d", base, placed.ID), nil, nil); status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated GET /api/orders/%d: status %d (want 401)", placed.ID, status)
	}

	// With a session: both reads answer 200 and the order is visible.
	var list struct {
		Orders []struct {
			ID            int64  `json:"id"`
			CustomerEmail string `json:"customer_email"`
		} `json:"orders"`
	}
	if status := doJSON(t, ops, http.MethodGet, base+"/api/orders", nil, &list); status != http.StatusOK {
		t.Fatalf("authenticated GET /api/orders: status %d (want 200)", status)
	}
	found := false
	for _, o := range list.Orders {
		if o.ID == placed.ID && o.CustomerEmail == customer {
			found = true
		}
	}
	if !found {
		t.Fatalf("order %d for %s missing from authenticated list", placed.ID, customer)
	}
	var one struct {
		CustomerEmail string `json:"customer_email"`
	}
	if status := doJSON(t, ops, http.MethodGet,
		fmt.Sprintf("%s/api/orders/%d", base, placed.ID), nil, &one); status != http.StatusOK {
		t.Fatalf("authenticated GET /api/orders/%d: status %d (want 200)", placed.ID, status)
	}
	if one.CustomerEmail != customer {
		t.Fatalf("authenticated GET /api/orders/%d: customer_email %q (want %q)", placed.ID, one.CustomerEmail, customer)
	}
}
