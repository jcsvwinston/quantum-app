package warehouse

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jcsvwinston/nucleus/pkg/nucleus"
	"github.com/jcsvwinston/nucleus/pkg/router"
)

const testOutboxSecret = "unit-test-outbox-secret"

// postHook drives outboxHook directly and returns the HTTP status it wrote.
// The body carries an UNKNOWN topic on purpose: once authenticated, the hook
// acks an unknown topic with 200 (processed:false) BEFORE any database access,
// so the module under test needs no DB and the status isolates the
// authentication decision — 401 = rejected at the door, 200 = accepted.
func postHook(t *testing.T, m *module, body []byte, headers map[string]string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/hooks/outbox", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	c := &nucleus.Context{Context: router.NewContext(rec, req, nil)}
	if err := m.outboxHook(c); err != nil {
		t.Fatalf("outboxHook returned error: %v", err)
	}
	return rec.Code
}

// TestOutboxHookRequiresSignature is the SEC-2 gate: /hooks/outbox authenticates
// EVERY delivery with the bridge's HMAC-SHA256 body signature and nothing else.
//
//   - a valid signature over the exact body is accepted;
//   - a request with NO X-Nucleus-Signature header is rejected 401 — even when
//     it carries a would-be legacy X-Outbox-Token (the removed fallback used
//     to accept that; its return is what the red/green demonstration guards);
//   - a garbage signature, a signature under the wrong secret, and a body
//     tampered by a single byte are all rejected 401.
func TestOutboxHookRequiresSignature(t *testing.T) {
	m := &module{
		deps: Deps{OutboxSecret: testOutboxSecret},
		log:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	body := []byte(`{"id":"sec2-1","topic":"unknown.topic","payload":null}`)
	sig := nucleus.SignWebhookBody(testOutboxSecret, body)

	// A single extra byte appended to the signed body: the HMAC no longer
	// matches the bytes on the wire.
	tampered := append(append([]byte{}, body...), ' ')

	cases := []struct {
		name    string
		body    []byte
		headers map[string]string
		want    int
	}{
		{"valid body signature is accepted", body,
			map[string]string{nucleus.WebhookSignatureHeader: sig}, http.StatusOK},
		{"no signature header is rejected", body,
			nil, http.StatusUnauthorized},
		{"no signature but a would-be legacy token is still rejected", body,
			map[string]string{"X-Outbox-Token": "dev-outbox-token"}, http.StatusUnauthorized},
		{"garbage signature is rejected", body,
			map[string]string{nucleus.WebhookSignatureHeader: "sha256=deadbeef"}, http.StatusUnauthorized},
		{"signature under the wrong secret is rejected", body,
			map[string]string{nucleus.WebhookSignatureHeader: nucleus.SignWebhookBody("not-the-secret", body)}, http.StatusUnauthorized},
		{"body tampered by one byte is rejected", tampered,
			map[string]string{nucleus.WebhookSignatureHeader: sig}, http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := postHook(t, m, tc.body, tc.headers); got != tc.want {
				t.Fatalf("%s: status %d (want %d)", tc.name, got, tc.want)
			}
		})
	}
}
