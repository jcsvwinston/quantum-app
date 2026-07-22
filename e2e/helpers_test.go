// Package e2e drives the REAL application binary over HTTP against real
// backing services (PostgreSQL primary + replica, MySQL, Redis, MinIO,
// Mailpit). Every test skips unless QA_APP_URL is set — scripts/e2e_run.sh
// boots the app and sets the environment; `go test ./...` without services
// stays green.
package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func envOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}

// appURL returns the base URL of the running app, skipping the test when the
// E2E environment is not present.
func appURL(t *testing.T) string {
	t.Helper()
	u := os.Getenv("QA_APP_URL")
	if u == "" {
		t.Skip("QA_APP_URL not set; run via scripts/e2e_run.sh (E2E needs the real app + services)")
	}
	return strings.TrimRight(u, "/")
}

// client returns an http.Client with a cookie jar (session-based flows).
func client(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}
	return &http.Client{
		Jar:     jar,
		Timeout: 15 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse // keep 3xx visible to assertions
		},
	}
}

// doJSON sends a JSON request and decodes a JSON response into out (when
// non-nil). Returns the status code.
func doJSON(t *testing.T, c *http.Client, method, rawurl string, body any, out any) int {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, rawurl, rd)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, rawurl, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("%s %s: decode %q: %v", method, rawurl, string(raw), err)
		}
	}
	return resp.StatusCode
}

// login authenticates the ops user and returns the session cookie value.
func login(t *testing.T, c *http.Client, base string) string {
	t.Helper()
	status := doJSON(t, c, http.MethodPost, base+"/api/login", map[string]string{
		"email":    envOr("QA_OPS_EMAIL", "ops@warehouse.local"),
		"password": envOr("QA_OPS_PASSWORD", "ci-e2e-ops-password"),
	}, nil)
	if status != http.StatusOK {
		t.Fatalf("login: status %d", status)
	}
	u, _ := url.Parse(base)
	for _, ck := range c.Jar.Cookies(u) {
		if ck.Name == "session" {
			return ck.Value
		}
	}
	t.Fatal("login: no session cookie set")
	return ""
}

// adminLogin authenticates against the orbit panel (form POST) using the
// bootstrap admin credentials; the session lands in the same cookie jar.
func adminLogin(t *testing.T, c *http.Client, base string) {
	t.Helper()
	form := url.Values{}
	form.Set("username", envOr("QA_ADMIN_USER", "admin"))
	form.Set("password", envOr("QA_ADMIN_PASSWORD", "warehouse-admin"))
	form.Set("next", "/admin/")
	resp, err := c.PostForm(base+"/admin/login", form)
	if err != nil {
		t.Fatalf("admin login: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("admin login: status %d (want 303): %.300s", resp.StatusCode, string(body))
	}
}

// uniqueSKU builds a per-run unique product SKU.
func uniqueSKU(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

// createProduct provisions a product through the public API (requires login).
func createProduct(t *testing.T, c *http.Client, base, sku string, stock int64) int64 {
	t.Helper()
	var created struct {
		ID int64 `json:"id"`
	}
	status := doJSON(t, c, http.MethodPost, base+"/api/products", map[string]any{
		"sku": sku, "name": "E2E " + sku, "price_cents": 1250, "stock": stock,
	}, &created)
	if status != http.StatusCreated || created.ID == 0 {
		t.Fatalf("create product: status %d id %d", status, created.ID)
	}
	return created.ID
}

// eventually polls fn until it returns nil or the deadline passes.
func eventually(t *testing.T, timeout time.Duration, what string, fn func() error) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last error
	for time.Now().Before(deadline) {
		if last = fn(); last == nil {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s: %v", timeout, what, last)
}
