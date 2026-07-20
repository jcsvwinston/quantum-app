package e2e

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestOrderOutboxAndMail exercises the transactional outbox on real
// PostgreSQL and real SMTP: placing an order commits the order row and the
// orders.placed event in ONE transaction; the framework's leasing dispatcher
// delivers the event to the app's webhook, which emails the customer through
// Mailpit and flips the order to confirmed. Asserted at every layer: order
// status over HTTP, outbox row status in PostgreSQL, message in Mailpit's API.
func TestOrderOutboxAndMail(t *testing.T) {
	base := appURL(t)
	c := client(t)
	mailpit := strings.TrimRight(envOr("QA_MAILPIT_API", "http://127.0.0.1:58025"), "/")

	login(t, c, base)
	id := createProduct(t, c, base, uniqueSKU("order"), 5)

	customer := fmt.Sprintf("customer+%d@example.test", time.Now().UnixNano())

	// Place the order.
	var placed struct {
		ID     int64  `json:"id"`
		Status string `json:"status"`
	}
	if status := doJSON(t, c, http.MethodPost, base+"/api/orders", map[string]any{
		"product_id": id, "quantity": 2, "customer_email": customer,
	}, &placed); status != http.StatusCreated {
		t.Fatalf("create order: status %d", status)
	}
	if placed.Status != "pending" {
		t.Fatalf("create order: status field %q (want pending)", placed.Status)
	}

	// Stock was decremented in the same transaction.
	var prod struct {
		Stock int64 `json:"stock"`
	}
	if status := doJSON(t, c, http.MethodGet, fmt.Sprintf("%s/api/products/%d", base, id), nil, &prod); status != http.StatusOK {
		t.Fatalf("get product: status %d", status)
	}
	if prod.Stock != 3 {
		t.Fatalf("stock after order = %d (want 3)", prod.Stock)
	}

	// The relay delivers: order flips to confirmed.
	eventually(t, 30*time.Second, "order confirmation via outbox relay", func() error {
		var got struct {
			Status string `json:"status"`
		}
		if status := doJSON(t, c, http.MethodGet, fmt.Sprintf("%s/api/orders/%d", base, placed.ID), nil, &got); status != http.StatusOK {
			return fmt.Errorf("get order: status %d", status)
		}
		if got.Status != "confirmed" {
			return fmt.Errorf("order status %q", got.Status)
		}
		return nil
	})

	// The outbox row in PostgreSQL is marked delivered.
	pgDSN := envOr("QA_PG_DSN", "postgres://warehouse:warehouse@127.0.0.1:55442/warehouse?sslmode=disable")
	pdb, err := sql.Open("pgx", pgDSN)
	if err != nil {
		t.Fatalf("open pg: %v", err)
	}
	defer pdb.Close()
	eventually(t, 15*time.Second, "outbox row delivered", func() error {
		var status string
		err := pdb.QueryRow(
			`SELECT status FROM nucleus_outbox WHERE topic = 'orders.placed' AND payload LIKE $1 ORDER BY created_at DESC LIMIT 1`,
			"%"+customer+"%").Scan(&status)
		if err != nil {
			return fmt.Errorf("outbox row: %w", err)
		}
		if status != "delivered" {
			return fmt.Errorf("outbox status %q", status)
		}
		return nil
	})

	// The confirmation email really left over SMTP: Mailpit received it.
	eventually(t, 15*time.Second, "confirmation email in Mailpit", func() error {
		resp, err := http.Get(mailpit + "/api/v1/search?query=" + customer)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		var res struct {
			Messages []struct {
				Subject string `json:"Subject"`
				To      []struct {
					Address string `json:"Address"`
				} `json:"To"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(raw, &res); err != nil {
			return fmt.Errorf("mailpit decode: %w (%.200s)", err, string(raw))
		}
		for _, m := range res.Messages {
			for _, to := range m.To {
				if to.Address == customer && strings.Contains(m.Subject, fmt.Sprintf("Order #%d", placed.ID)) {
					return nil
				}
			}
		}
		return fmt.Errorf("no message for %s among %d results", customer, len(res.Messages))
	})

	// Ordering more than the remaining stock is refused atomically.
	if status := doJSON(t, c, http.MethodPost, base+"/api/orders", map[string]any{
		"product_id": id, "quantity": 99, "customer_email": customer,
	}, nil); status != http.StatusConflict {
		t.Fatalf("oversell order: status %d (want 409)", status)
	}
}
