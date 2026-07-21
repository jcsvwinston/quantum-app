// Unit tests for the pure request-handling logic of the warehouse module:
// ID parsing, input validation, and read-path selection. They run without any
// backing service (the E2E suite covers the wired paths); this is what the CI
// "Unit tests" step actually executes on every push.
package warehouse

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jcsvwinston/nucleus/pkg/nucleus"
	"github.com/jcsvwinston/nucleus/pkg/router"
	"github.com/jcsvwinston/quark"
)

// testContext builds a *nucleus.Context around a recorder and request the way
// the router does at runtime; id, when non-empty, becomes the {id} path value.
func testContext(w http.ResponseWriter, r *http.Request, id string) *nucleus.Context {
	if id != "" {
		r.SetPathValue("id", id)
	}
	return &nucleus.Context{Context: router.NewContext(w, r, nil)}
}

func TestParseID(t *testing.T) {
	cases := []struct {
		name   string
		raw    string
		wantID int64
		wantOK bool
	}{
		{"valid", "42", 42, true},
		{"zero is invalid", "0", 0, false},
		{"negative is invalid", "-3", 0, false},
		{"non-numeric", "abc", 0, false},
		{"empty", "", 0, false},
		{"overflow", "9223372036854775808", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c := testContext(rec, httptest.NewRequest(http.MethodGet, "/api/products/x", nil), tc.raw)
			id, ok := parseID(c)
			if id != tc.wantID || ok != tc.wantOK {
				t.Fatalf("parseID(%q) = (%d, %v), want (%d, %v)", tc.raw, id, ok, tc.wantID, tc.wantOK)
			}
			if !tc.wantOK {
				// The helper writes the 400 itself; the handler just returns.
				if rec.Code != http.StatusBadRequest {
					t.Fatalf("parseID(%q): wrote status %d (want 400)", tc.raw, rec.Code)
				}
			}
		})
	}
}

// TestCreateOrderValidation drives createOrder through its input validation:
// every rejection must happen before the handler touches the database (the
// module under test has no DB, so reaching it would panic — that is the test).
func TestCreateOrderValidation(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		wantCode int
		wantErr  string
	}{
		{"invalid JSON", "{not json", http.StatusBadRequest, "invalid JSON"},
		{"missing everything", "{}", http.StatusBadRequest, "required"},
		{"zero product", `{"product_id":0,"quantity":1,"customer_email":"a@b.test"}`, http.StatusBadRequest, "required"},
		{"negative quantity", `{"product_id":1,"quantity":-2,"customer_email":"a@b.test"}`, http.StatusBadRequest, "required"},
		{"empty email", `{"product_id":1,"quantity":1,"customer_email":""}`, http.StatusBadRequest, "required"},
	}
	m := &module{} // no DB, no outbox: validation must reject first
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/orders", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			if err := m.createOrder(testContext(rec, req, "")); err != nil {
				t.Fatalf("createOrder: %v", err)
			}
			if rec.Code != tc.wantCode {
				t.Fatalf("createOrder(%s): status %d (want %d)", tc.name, rec.Code, tc.wantCode)
			}
			if !strings.Contains(rec.Body.String(), tc.wantErr) {
				t.Fatalf("createOrder(%s): body %q (want %q mentioned)", tc.name, rec.Body.String(), tc.wantErr)
			}
		})
	}
}

func TestReadPath(t *testing.T) {
	primary := &quark.Client{}
	replica := &quark.Client{}

	same := &module{deps: Deps{PG: primary, PGRead: primary}}
	if got := same.readPath(); got != "primary" {
		t.Fatalf("readPath with PG == PGRead = %q (want primary)", got)
	}
	split := &module{deps: Deps{PG: primary, PGRead: replica}}
	if got := split.readPath(); got != "replica" {
		t.Fatalf("readPath with PG != PGRead = %q (want replica)", got)
	}
}
