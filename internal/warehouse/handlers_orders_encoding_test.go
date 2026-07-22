package warehouse

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jcsvwinston/nucleus/pkg/nucleus"
	"github.com/jcsvwinston/nucleus/pkg/outbox"
	"github.com/jcsvwinston/nucleus/pkg/router"
)

// postHookResp is postHook's sibling that also returns the response body, so a
// test can tell WHICH gate rejected a delivery (both the encoding mismatch and
// a later bad payload write 400 — the error string disambiguates them).
func postHookResp(t *testing.T, m *module, body []byte, headers map[string]string) (int, string) {
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
	return rec.Code, rec.Body.String()
}

// TestOutboxHookChecksPayloadEncoding is the SEC-3 gate: the consumer decodes
// by the encoding it is CONFIGURED to expect (Deps.OutboxEncoding) and rejects
// a delivery whose unsigned X-Outbox-Payload-Encoding header disagrees with it
// — the header can never steer the decode.
//
// The order-placed topic is used on purpose so the delivery reaches the
// encoding gate (an unknown topic is acked at 200 before it). Auth is always a
// valid body signature, isolating the encoding decision. The bodies carry a
// zero order_id, so a delivery that PASSES the encoding gate stops one step
// later at "invalid payload": that different error is exactly how the test
// distinguishes "rejected at the encoding gate" from "got past it".
func TestOutboxHookChecksPayloadEncoding(t *testing.T) {
	newModule := func(configured string) *module {
		return &module{
			deps: Deps{OutboxSecret: testOutboxSecret, OutboxEncoding: configured},
			log:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		}
	}
	signed := func(m *module, header string) (int, string) {
		body := []byte(`{"id":"sec3-1","topic":"` + orderPlacedTopic + `","payload":{"order_id":0}}`)
		headers := map[string]string{
			nucleus.WebhookSignatureHeader: nucleus.SignWebhookBody(m.deps.OutboxSecret, body),
		}
		if header != "" {
			headers[outbox.WebhookPayloadEncodingHeader] = header
		}
		return postHookResp(t, m, body, headers)
	}

	cases := []struct {
		name       string
		configured string
		header     string
		wantStatus int
		wantBody   string // substring the error message must contain
	}{
		{"declared json matches configured json → past the gate", "json", "json",
			http.StatusBadRequest, "invalid payload"},
		{"declared base64 against configured json → rejected at the gate", "json", "base64",
			http.StatusBadRequest, "payload encoding mismatch"},
		{"absent header defaults to base64, against configured json → rejected", "json", "",
			http.StatusBadRequest, "payload encoding mismatch"},
		{"declared json against configured base64 → rejected at the gate", "base64", "json",
			http.StatusBadRequest, "payload encoding mismatch"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, body := signed(newModule(tc.configured), tc.header)
			if status != tc.wantStatus {
				t.Fatalf("status %d (want %d); body %s", status, tc.wantStatus, body)
			}
			if !strings.Contains(body, tc.wantBody) {
				t.Fatalf("body %q does not contain %q", body, tc.wantBody)
			}
		})
	}
}
