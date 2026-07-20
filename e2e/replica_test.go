package e2e

import (
	"database/sql"
	"fmt"
	"net/http"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestReadReplica exercises quark's read-replica surface (WithReplicas)
// against a real PostgreSQL streaming replica:
//  1. pg_stat_replication on the primary proves a standby is streaming.
//  2. A product created on the primary becomes visible on the replica (direct
//     SQL against the standby) — real replication, not a shared connection.
//  3. The app's /api/reports/inventory reads through the replica-routed
//     client and eventually sees the product.
func TestReadReplica(t *testing.T) {
	base := appURL(t)
	c := client(t)

	replicaDSN := envOr("QA_PG_REPLICA_DSN", "postgres://warehouse:warehouse@127.0.0.1:55443/warehouse?sslmode=disable")
	primaryDSN := envOr("QA_PG_DSN", "postgres://warehouse:warehouse@127.0.0.1:55442/warehouse?sslmode=disable")

	primary, err := sql.Open("pgx", primaryDSN)
	if err != nil {
		t.Fatalf("open primary: %v", err)
	}
	defer primary.Close()
	replica, err := sql.Open("pgx", replicaDSN)
	if err != nil {
		t.Fatalf("open replica: %v", err)
	}
	defer replica.Close()

	// 1. The standby is streaming from the primary.
	var streaming int
	if err := primary.QueryRow(
		"SELECT COUNT(*) FROM pg_stat_replication WHERE state = 'streaming'").Scan(&streaming); err != nil {
		t.Fatalf("pg_stat_replication: %v", err)
	}
	if streaming == 0 {
		t.Fatal("no streaming standby attached to the primary")
	}
	// And the replica is in recovery (it is a standby, not another primary).
	var inRecovery bool
	if err := replica.QueryRow("SELECT pg_is_in_recovery()").Scan(&inRecovery); err != nil {
		t.Fatalf("pg_is_in_recovery: %v", err)
	}
	if !inRecovery {
		t.Fatal("replica is not in recovery mode")
	}

	login(t, c, base)
	sku := uniqueSKU("replica")
	id := createProduct(t, c, base, sku, 11)

	// 2. The row reaches the standby.
	eventually(t, 15*time.Second, "row on the replica", func() error {
		var n int
		if err := replica.QueryRow("SELECT COUNT(*) FROM products WHERE id = $1", id).Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("product %d not on replica yet", id)
		}
		return nil
	})

	// 3. The app's replica-routed read path serves it.
	eventually(t, 15*time.Second, "inventory report through the replica", func() error {
		var report struct {
			ReadPath string `json:"read_path"`
			Items    []struct {
				SKU   string `json:"sku"`
				Stock int64  `json:"stock"`
			} `json:"items"`
		}
		if status := doJSON(t, c, http.MethodGet, base+"/api/reports/inventory", nil, &report); status != http.StatusOK {
			return fmt.Errorf("report: status %d", status)
		}
		if report.ReadPath != "replica" {
			return fmt.Errorf("read_path %q (want replica)", report.ReadPath)
		}
		for _, it := range report.Items {
			if it.SKU == sku && it.Stock == 11 {
				return nil
			}
		}
		return fmt.Errorf("sku %s not in report yet", sku)
	})
}
