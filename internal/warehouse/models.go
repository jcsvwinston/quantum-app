// Package warehouse is the application domain: a small inventory-and-orders
// service. Products, orders, and application users live on PostgreSQL (the
// default database); stock movements are an append-only audit trail on the
// aliased MySQL database. All four are Quark models, so the orbit admin panel
// can browse and edit them through the quarkdatasource adapter.
package warehouse

import (
	"context"
	"time"
)

// Product is something the warehouse stocks. Lives on PostgreSQL.
type Product struct {
	ID            int64     `db:"id" pk:"true" json:"id"`
	SKU           string    `db:"sku" quark:"unique,not_null" json:"sku"`
	Name          string    `db:"name" quark:"not_null" json:"name"`
	PriceCents    int64     `db:"price_cents" quark:"not_null" json:"price_cents"`
	Stock         int64     `db:"stock" json:"stock"`
	AttachmentKey string    `db:"attachment_key" json:"attachment_key"`
	CreatedAt     time.Time `db:"created_at" json:"created_at"`
}

// BeforeCreate stamps the creation time.
func (p *Product) BeforeCreate(ctx context.Context) error {
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now().UTC()
	}
	return nil
}

// Order references a product. Lives on PostgreSQL. Orders are inserted inside
// the same SQL transaction that enqueues the outbox event announcing them
// (see module.go: createOrder), and flip from "pending" to "confirmed" when
// the outbox relay delivers that event back to the app's webhook.
type Order struct {
	ID            int64     `db:"id" pk:"true" json:"id"`
	ProductID     int64     `db:"product_id" quark:"not_null" json:"product_id"`
	Quantity      int64     `db:"quantity" quark:"not_null" json:"quantity"`
	CustomerEmail string    `db:"customer_email" quark:"not_null" json:"customer_email"`
	Status        string    `db:"status" quark:"not_null" json:"status"`
	CreatedAt     time.Time `db:"created_at" json:"created_at"`

	Product Product `rel:"belongs_to" join:"product_id" json:"-"`
}

// AppUser is an application login (not an orbit admin user). Lives on
// PostgreSQL. Passwords are bcrypt hashes via nucleus pkg/auth.
type AppUser struct {
	ID           int64  `db:"id" pk:"true" json:"id"`
	Email        string `db:"email" quark:"unique,not_null" json:"email"`
	PasswordHash string `db:"password_hash" quark:"not_null" json:"-"`
	Name         string `db:"name" json:"name"`
}

// StockMovement is the append-only audit trail of stock changes. Lives on the
// aliased MySQL database ("audit" in the nucleus config), written through a
// dedicated Quark client, so the app exercises Quark CRUD against two engines.
type StockMovement struct {
	ID        int64     `db:"id" pk:"true" json:"id"`
	ProductID int64     `db:"product_id" quark:"not_null" json:"product_id"`
	Delta     int64     `db:"delta" quark:"not_null" json:"delta"`
	Reason    string    `db:"reason" quark:"not_null" json:"reason"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
}

// BeforeCreate stamps the creation time.
func (s *StockMovement) BeforeCreate(ctx context.Context) error {
	if s.CreatedAt.IsZero() {
		s.CreatedAt = time.Now().UTC()
	}
	return nil
}
