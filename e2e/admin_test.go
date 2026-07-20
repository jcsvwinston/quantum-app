package e2e

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestOrbitPanelDataStudio exercises the quark→quarkbridge→orbit chain over
// HTTP: log into the orbit panel with the bootstrap admin, list the quark
// models in Data Studio, create and edit a Product THROUGH THE PANEL, and
// verify the changes through the public API (same database, same models).
func TestOrbitPanelDataStudio(t *testing.T) {
	base := appURL(t)
	c := client(t)

	// Unauthenticated panel API is redirected to login.
	resp, err := c.Get(base + "/admin/api/models")
	if err != nil {
		t.Fatalf("panel unauthenticated: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatal("panel API answered without a session")
	}

	adminLogin(t, c, base)

	// Data Studio lists the registered quark models.
	var models struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if status := doJSON(t, c, http.MethodGet, base+"/admin/api/models", nil, &models); status != http.StatusOK {
		t.Fatalf("list models: status %d", status)
	}
	have := map[string]bool{}
	for _, m := range models.Models {
		have[m.Name] = true
	}
	if !have["Product"] || !have["Order"] {
		t.Fatalf("Data Studio models = %+v (want Product and Order)", models.Models)
	}

	// Create a product THROUGH the panel.
	sku := uniqueSKU("studio")
	var created map[string]any
	if status := doJSON(t, c, http.MethodPost, base+"/admin/api/models/Product", map[string]any{
		"sku": sku, "name": "Studio " + sku, "price_cents": 4200, "stock": 3,
	}, &created); status != http.StatusCreated {
		t.Fatalf("panel create: status %d (%v)", status, created)
	}
	id := int64(0)
	if v, ok := created["id"].(float64); ok {
		id = int64(v)
	}
	if id == 0 {
		t.Fatalf("panel create: no id in %v", created)
	}

	// The public API sees it (same quark models, same PostgreSQL).
	var got struct {
		SKU  string `json:"sku"`
		Name string `json:"name"`
	}
	if status := doJSON(t, c, http.MethodGet, fmt.Sprintf("%s/api/products/%d", base, id), nil, &got); status != http.StatusOK {
		t.Fatalf("api get after panel create: status %d", status)
	}
	if got.SKU != sku {
		t.Fatalf("api get after panel create: %+v", got)
	}

	// Edit it THROUGH the panel.
	if status := doJSON(t, c, http.MethodPut, fmt.Sprintf("%s/admin/api/models/Product/%d", base, id), map[string]any{
		"name": "Edited in Data Studio",
	}, nil); status != http.StatusOK {
		t.Fatalf("panel update: status %d", status)
	}
	if status := doJSON(t, c, http.MethodGet, fmt.Sprintf("%s/api/products/%d", base, id), nil, &got); status != http.StatusOK {
		t.Fatalf("api get after panel edit: status %d", status)
	}
	if got.Name != "Edited in Data Studio" {
		t.Fatalf("api get after panel edit: name %q", got.Name)
	}

	// Panel list endpoint pages the records.
	var page struct {
		Items []map[string]any `json:"items"`
		Total int64            `json:"total"`
	}
	if status := doJSON(t, c, http.MethodGet, base+"/admin/api/models/Product?page=1&page_size=50", nil, &page); status != http.StatusOK {
		t.Fatalf("panel list records: status %d", status)
	}
	if page.Total < 1 {
		t.Fatalf("panel list records: total %d", page.Total)
	}

	// Delete it THROUGH the panel; the public API no longer sees it.
	if status := doJSON(t, c, http.MethodDelete, fmt.Sprintf("%s/admin/api/models/Product/%d", base, id), nil, nil); status != http.StatusOK && status != http.StatusNoContent {
		t.Fatalf("panel delete: status %d", status)
	}
	if status := doJSON(t, c, http.MethodGet, fmt.Sprintf("%s/api/products/%d", base, id), nil, nil); status != http.StatusNotFound {
		t.Fatalf("api get after panel delete: status %d (want 404)", status)
	}
}

// TestOrbitLiveFeed exercises the live observability feed for real: API
// traffic runs quark SQL through orbit/quarkbridge onto the nucleus event
// bus; the panel consumes the bus in-process and serves the feed at
// /admin/api/live/snapshot. The test generates traffic and asserts the SQL
// events show up, correlated to requests.
func TestOrbitLiveFeed(t *testing.T) {
	base := appURL(t)
	c := client(t)

	adminLogin(t, c, base)

	// Generate real traffic on the public API (each request runs quark SQL
	// through the bridged client).
	probe := client(t)
	login(t, probe, base)
	sku := uniqueSKU("live")
	createProduct(t, probe, base, sku, 2)
	for i := 0; i < 3; i++ {
		if status := doJSON(t, probe, http.MethodGet, base+"/api/products", nil, nil); status != http.StatusOK {
			t.Fatalf("traffic: status %d", status)
		}
	}

	// The SQL shows up on the live feed with request correlation.
	eventually(t, 15*time.Second, "quark SQL on the live feed", func() error {
		var snap struct {
			Enabled bool `json:"enabled"`
			Queries []struct {
				Query     string `json:"query"`
				Operation string `json:"operation"`
				RequestID string `json:"request_id"`
			} `json:"queries"`
		}
		if status := doJSON(t, c, http.MethodGet, base+"/admin/api/live/snapshot?sql_limit=200", nil, &snap); status != http.StatusOK {
			return fmt.Errorf("snapshot: status %d", status)
		}
		if !snap.Enabled {
			return fmt.Errorf("live feed disabled")
		}
		var productSQL, correlated bool
		for _, q := range snap.Queries {
			if strings.Contains(strings.ToLower(q.Query), "products") {
				productSQL = true
				if q.RequestID != "" {
					correlated = true
				}
			}
		}
		if !productSQL {
			return fmt.Errorf("no products SQL among %d events", len(snap.Queries))
		}
		if !correlated {
			return fmt.Errorf("products SQL present but no request correlation")
		}
		return nil
	})

	// Bonus surface: the system snapshot answers too.
	if status := doJSON(t, c, http.MethodGet, base+"/admin/api/system/snapshot", nil, nil); status != http.StatusOK {
		t.Fatalf("system snapshot: status %d", status)
	}
}
