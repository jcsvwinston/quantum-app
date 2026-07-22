package warehouse

import (
	"crypto/hmac"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/jcsvwinston/nucleus/pkg/mail"
	"github.com/jcsvwinston/nucleus/pkg/nucleus"
	"github.com/jcsvwinston/nucleus/pkg/outbox"
	"github.com/jcsvwinston/quark"
)

// orderPlacedTopic is the outbox topic for new orders; the config routes
// "orders.*" to the webhook bridge pointing back at /hooks/outbox.
const orderPlacedTopic = "orders.placed"

// orderPlacedPayload is the outbox event body.
type orderPlacedPayload struct {
	OrderID       int64  `json:"order_id"`
	ProductID     int64  `json:"product_id"`
	Quantity      int64  `json:"quantity"`
	CustomerEmail string `json:"customer_email"`
}

// createOrder is the transactional-outbox write: one SQL transaction on the
// framework-managed pool decrements stock, inserts the order as "pending",
// and enqueues the orders.placed event. Either all three commit or none do.
// The framework's outbox dispatcher then delivers the event to /hooks/outbox,
// which emails the customer and flips the order to "confirmed".
func (m *module) createOrder(c *nucleus.Context) error {
	var in struct {
		ProductID     int64  `json:"product_id"`
		Quantity      int64  `json:"quantity"`
		CustomerEmail string `json:"customer_email"`
	}
	if err := c.BindJSON(&in); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
	}
	if in.ProductID <= 0 || in.Quantity <= 0 || in.CustomerEmail == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "product_id, quantity and customer_email are required"})
	}

	ctx := c.Request.Context()
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx,
		"UPDATE products SET stock = stock - $1 WHERE id = $2 AND stock >= $1",
		in.Quantity, in.ProductID)
	if err != nil {
		return err
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return c.JSON(http.StatusConflict, map[string]string{"error": "unknown product or insufficient stock"})
	}

	var orderID int64
	if err := tx.QueryRowContext(ctx,
		"INSERT INTO orders (product_id, quantity, customer_email, status, created_at) VALUES ($1, $2, $3, 'pending', NOW()) RETURNING id",
		in.ProductID, in.Quantity, in.CustomerEmail).Scan(&orderID); err != nil {
		return err
	}

	if _, err := m.outbox.EnqueueTx(ctx, tx, outbox.Entry{
		Topic: orderPlacedTopic,
		Payload: orderPlacedPayload{
			OrderID:       orderID,
			ProductID:     in.ProductID,
			Quantity:      in.Quantity,
			CustomerEmail: in.CustomerEmail,
		},
	}); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	// Audit trail on MySQL, after commit (best-effort, separate database).
	mv := StockMovement{ProductID: in.ProductID, Delta: -in.Quantity, Reason: fmt.Sprintf("order:%d", orderID)}
	if err := quark.For[StockMovement](ctx, m.bridgedMy).Create(&mv); err != nil {
		m.log.Error("warehouse: audit write failed", "order_id", orderID, "error", err)
	}

	return c.JSON(http.StatusCreated, map[string]any{"id": orderID, "status": "pending"})
}

// listOrders and getOrder require a session: orders carry customer_email
// (PII), so unlike the product catalogue these reads are operator-only.
func (m *module) listOrders(c *nucleus.Context) error {
	if !m.requireUser(c) {
		return nil
	}
	orders, err := quark.For[Order](c.Request.Context(), m.bridgedPG).
		OrderBy("id", "DESC").List()
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"orders": orders, "count": len(orders)})
}

func (m *module) getOrder(c *nucleus.Context) error {
	if !m.requireUser(c) {
		return nil
	}
	id, ok := parseID(c)
	if !ok {
		return nil
	}
	o, err := quark.For[Order](c.Request.Context(), m.bridgedPG).Find(id)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "order not found"})
	}
	return c.JSON(http.StatusOK, o)
}

// payloadEncodingHeader mirrors outbox.WebhookPayloadEncodingHeader: the
// bridge declares on every delivery whether the "payload" field is embedded
// JSON ("json") or a base64 JSON string ("base64"). The constant is local
// because the pinned nucleus release predates the header; switch to the
// exported outbox constant when the pins move past it.
const payloadEncodingHeader = "X-Outbox-Payload-Encoding"

// authenticateOutboxDelivery authenticates one delivery to /hooks/outbox
// against the bridge's HMAC-SHA256 body signature — the only accepted proof.
//
// The request MUST carry nucleus.WebhookSignatureHeader (the same header
// module webhooks use); its value must verify against the shared secret over
// the exact body bytes, compared with hmac.Equal, never with != (a
// non-constant-time comparison leaks how many leading bytes matched). A
// request with no signature header authenticates with nothing and is
// rejected.
//
// There is no static-token fallback: the pinned nucleus (v1.5.0) signs every
// bridge delivery, so accepting an unsigned request on a shared token would
// only add a second, weaker credential and collapse the door's strength to
// min(HMAC, token). The signature is over the body only — exactly what the
// pinned nucleus signs.
func (m *module) authenticateOutboxDelivery(r *http.Request, body []byte) bool {
	sig := r.Header.Get(nucleus.WebhookSignatureHeader)
	if sig == "" {
		return false
	}
	want := nucleus.SignWebhookBody(m.deps.OutboxSecret, body)
	return hmac.Equal([]byte(want), []byte(sig))
}

// decodeOutboxPayload decodes the "payload" field of a delivery according to
// the encoding the bridge declared, instead of guessing the shape:
//
//   - "json": the payload document is embedded verbatim — use it as is.
//   - "base64": the field is a JSON string holding base64 of the payload
//     bytes (the classic wire shape; also what an absent header means, since
//     nucleus releases up to v1.4.0 emit that shape and no header).
func decodeOutboxPayload(encoding string, field json.RawMessage) ([]byte, error) {
	switch encoding {
	case "json":
		return field, nil
	case "base64", "":
		var raw []byte // encoding/json base64-decodes into []byte
		if err := json.Unmarshal(field, &raw); err != nil {
			return nil, fmt.Errorf("payload is not the declared base64 string: %w", err)
		}
		return raw, nil
	default:
		return nil, fmt.Errorf("unknown %s value %q", payloadEncodingHeader, encoding)
	}
}

// outboxHook is the delivery target of the framework's outbox webhook bridge.
// Deliveries are authenticated by the bridge's HMAC body signature (required;
// see authenticateOutboxDelivery) and the payload is decoded per the declared
// X-Outbox-Payload-Encoding. Delivery is at-least-once, so the handler is
// idempotent: an already-confirmed order is a no-op.
func (m *module) outboxHook(c *nucleus.Context) error {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "unreadable body"})
	}
	if !m.authenticateOutboxDelivery(c.Request, body) {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "bad outbox signature"})
	}

	var msg struct {
		ID      string          `json:"id"`
		Topic   string          `json:"topic"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(body, &msg); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
	}
	if msg.Topic != orderPlacedTopic {
		// Unknown topics are acknowledged (2xx) so the dispatcher does not
		// retry an event this app will never handle.
		m.log.Warn("warehouse: outbox hook ignoring topic", "topic", msg.Topic, "message_id", msg.ID)
		return c.JSON(http.StatusOK, map[string]any{"processed": false})
	}

	payload, err := decodeOutboxPayload(c.Request.Header.Get(payloadEncodingHeader), msg.Payload)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid payload"})
	}
	var ev orderPlacedPayload
	if err := json.Unmarshal(payload, &ev); err != nil || ev.OrderID == 0 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid payload"})
	}

	ctx := c.Request.Context()
	o, err := quark.For[Order](ctx, m.bridgedPG).Find(ev.OrderID)
	if err != nil {
		return err
	}
	if o.Status != "pending" {
		return c.JSON(http.StatusOK, map[string]any{"processed": false, "status": o.Status})
	}

	// Email first, then confirm: a mail failure returns 5xx so the
	// dispatcher retries; a duplicate delivery after a confirm race is
	// caught by the status check above.
	err = m.mailer.Send(ctx, mail.Message{
		From:    m.deps.MailFrom,
		To:      []string{ev.CustomerEmail},
		Subject: fmt.Sprintf("Order #%d confirmed", ev.OrderID),
		Body: fmt.Sprintf(
			"Your order #%d (product %d, quantity %d) has been confirmed.\n",
			ev.OrderID, ev.ProductID, ev.Quantity),
	})
	if err != nil {
		return fmt.Errorf("warehouse: confirmation mail for order %d: %w", ev.OrderID, err)
	}

	if _, err := quark.For[Order](ctx, m.bridgedPG).
		Where("id", "=", ev.OrderID).
		Where("status", "=", "pending").
		UpdateMap(map[string]any{"status": "confirmed"}); err != nil {
		return err
	}

	m.log.Info("warehouse: order confirmed via outbox relay", "order_id", ev.OrderID, "message_id", msg.ID)
	return c.JSON(http.StatusOK, map[string]any{"processed": true})
}
