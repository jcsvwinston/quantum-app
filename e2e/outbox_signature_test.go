package e2e

import (
	"bytes"
	"context"
	"crypto/hmac"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jcsvwinston/nucleus/pkg/nucleus"
	"github.com/jcsvwinston/nucleus/pkg/outbox"
)

// payloadEncodingHeader mirrors the bridge's X-Outbox-Payload-Encoding
// declaration (exported by nucleus pkg/outbox after v1.4.0; local constant
// while the pins predate it).
const payloadEncodingHeader = "X-Outbox-Payload-Encoding"

func outboxSecret() string { return envOr("QA_OUTBOX_SECRET", "dev-outbox-secret") }

// postRaw POSTs body with the given extra headers and returns status + body.
func postRaw(t *testing.T, url string, body []byte, headers map[string]string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

// TestOutboxHookSignatureVerification drives the CONSUMER side of the
// webhook-bridge contract directly: /hooks/outbox must accept a delivery
// whose HMAC-SHA256 body signature (X-Nucleus-Signature, the module-webhook
// scheme) verifies under the shared secret, and reject — 401, before any
// processing — an invalid signature, a signature under the wrong secret, and
// an unsigned delivery with a bad legacy token. This runs against the pinned
// nucleus today: verification is the app's own code, independent of whether
// the bridge signs yet.
func TestOutboxHookSignatureVerification(t *testing.T) {
	base := appURL(t)
	hook := base + "/hooks/outbox"

	// An unknown topic exercises the full auth path with no side effects
	// (the hook acks unknown topics with processed:false).
	body := []byte(`{"id":"e2e-sig-1","topic":"e2e.signature.probe","payload":null}`)

	// Valid signature over the exact body -> accepted.
	sig := nucleus.SignWebhookBody(outboxSecret(), body)
	if status, resp := postRaw(t, hook, body, map[string]string{nucleus.WebhookSignatureHeader: sig}); status != http.StatusOK {
		t.Fatalf("valid signature: status %d (want 200), body %s", status, resp)
	}

	// Same signature over a TAMPERED body -> 401.
	tampered := bytes.Replace(body, []byte(`"e2e-sig-1"`), []byte(`"e2e-sig-2"`), 1)
	if status, _ := postRaw(t, hook, tampered, map[string]string{nucleus.WebhookSignatureHeader: sig}); status != http.StatusUnauthorized {
		t.Fatalf("tampered body under old signature: status %d (want 401)", status)
	}

	// Garbage signature -> 401.
	if status, _ := postRaw(t, hook, body, map[string]string{nucleus.WebhookSignatureHeader: "sha256=deadbeef"}); status != http.StatusUnauthorized {
		t.Fatalf("garbage signature: status %d (want 401)", status)
	}

	// Signature under the wrong secret -> 401.
	wrong := nucleus.SignWebhookBody("not-the-secret", body)
	if status, _ := postRaw(t, hook, body, map[string]string{nucleus.WebhookSignatureHeader: wrong}); status != http.StatusUnauthorized {
		t.Fatalf("wrong-secret signature: status %d (want 401)", status)
	}

	// Unsigned delivery with a bad legacy token -> 401.
	if status, _ := postRaw(t, hook, body, map[string]string{"X-Outbox-Token": "wrong-token"}); status != http.StatusUnauthorized {
		t.Fatalf("unsigned + bad token: status %d (want 401)", status)
	}
}

// TestOutboxBridgeSignsDeliveries drives the PRODUCER side end to end: an
// event enqueued in the real outbox table must arrive at the e2e-probe
// bridge target signed (HMAC-SHA256 over the exact body, verified here with
// hmac.Equal) and with X-Outbox-Payload-Encoding declaring the payload
// shape.
//
// GATED: nucleus releases up to the pinned v1.4.0 sign nothing and send no
// encoding header. When the delivery arrives without a signature the test
// SKIPS with the reason printed — it activates by itself when the suite pins
// move to a nucleus whose bridge signs (verified end to end locally against
// that nucleus; see the PR).
func TestOutboxBridgeSignsDeliveries(t *testing.T) {
	appURL(t) // skip without the E2E environment (the app runs the dispatcher)

	probeAddr := envOr("QA_OUTBOX_PROBE_ADDR", "127.0.0.1:58091")

	type capture struct {
		body    []byte
		headers http.Header
	}
	got := make(chan capture, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/probe", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read probe body: %v", err)
		}
		select {
		case got <- capture{body: body, headers: r.Header.Clone()}:
		default:
		}
		w.WriteHeader(http.StatusNoContent)
	})
	ln, err := net.Listen("tcp", probeAddr)
	if err != nil {
		t.Fatalf("probe listener on %s (must match the e2e-probe bridge url in config/e2e.yaml): %v", probeAddr, err)
	}
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	// Enqueue a probe event in the REAL outbox table; the app's dispatcher
	// routes e2eprobe.* to the e2e-probe bridge.
	pgDSN := envOr("QA_PG_DSN", "postgres://warehouse:warehouse@127.0.0.1:55442/warehouse?sslmode=disable")
	pdb, err := sql.Open("pgx", pgDSN)
	if err != nil {
		t.Fatalf("open pg: %v", err)
	}
	defer pdb.Close()
	store, err := outbox.NewStore(pdb, outbox.Config{TableName: outbox.DefaultTableName, Flavor: outbox.FlavorPostgres})
	if err != nil {
		t.Fatalf("outbox store: %v", err)
	}
	probe := fmt.Sprintf("probe-%d", time.Now().UnixNano())
	if _, err := store.Enqueue(context.Background(), outbox.Entry{
		Topic:   "e2eprobe.ping",
		Payload: map[string]string{"probe": probe},
	}); err != nil {
		t.Fatalf("enqueue probe event: %v", err)
	}

	var delivery capture
	select {
	case delivery = <-got:
	case <-time.After(45 * time.Second):
		t.Fatal("the dispatcher never delivered the probe event to the e2e-probe bridge")
	}

	sig := delivery.headers.Get(nucleus.WebhookSignatureHeader)
	if sig == "" {
		t.Skipf("SKIP: the outbox bridge does not sign deliveries (no %s header) — the pinned nucleus predates the bridge signing contract; this test activates when the suite pins move to a signing nucleus", nucleus.WebhookSignatureHeader)
	}

	// Signature verified over the EXACT body bytes, constant-time.
	want := nucleus.SignWebhookBody(outboxSecret(), delivery.body)
	if !hmac.Equal([]byte(want), []byte(sig)) {
		t.Fatalf("bridge signature %q does not verify over the delivered body (want %q)", sig, want)
	}

	// The encoding header must be present and truthful. The e2e-probe bridge
	// stays on the default shape, so the declaration must be base64 and the
	// payload must decode accordingly.
	encoding := delivery.headers.Get(payloadEncodingHeader)
	if encoding == "" {
		t.Fatalf("delivery carries no %s header", payloadEncodingHeader)
	}
	var msg struct {
		Topic   string          `json:"topic"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(delivery.body, &msg); err != nil {
		t.Fatalf("decode delivery body %q: %v", delivery.body, err)
	}
	if msg.Topic != "e2eprobe.ping" {
		t.Fatalf("delivered topic %q, want e2eprobe.ping", msg.Topic)
	}
	var payload []byte
	switch encoding {
	case "base64":
		if err := json.Unmarshal(msg.Payload, &payload); err != nil {
			t.Fatalf("payload declared base64 but is not a base64 JSON string: %v", err)
		}
	case "json":
		payload = msg.Payload
	default:
		t.Fatalf("unknown declared encoding %q", encoding)
	}
	if !strings.Contains(string(payload), probe) {
		t.Fatalf("decoded payload %q does not contain the probe marker %q", payload, probe)
	}
}
