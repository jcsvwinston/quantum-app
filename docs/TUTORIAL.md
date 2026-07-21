# Building a warehouse service with the Quantum suite

This walkthrough builds a small but real application — an inventory-and-orders
service — the way an external team would: [nucleus](https://github.com/jcsvwinston/nucleus)
as the web framework, [quark](https://github.com/jcsvwinston/quark) models for
the data layer, and the [orbit](https://github.com/jcsvwinston/orbit) admin
panel mounted in-process. Everything resolves from the Go module proxy at
released tags; no local checkouts of the suite are involved.

The finished result is this repository. Each step below points at the real
files, so you can read along or reproduce from scratch.

## 0. Prerequisites

- Go 1.26+
- Docker (for PostgreSQL, MySQL, Redis, MinIO and an SMTP catcher)

## 1. Start a module and pin the suite

```bash
mkdir warehouse && cd warehouse
go mod init example.com/warehouse
```

Add the suite at exact released versions. Pinning a coherent set matters: these
five tags are certified together upstream (see the suite's `versions.yaml`),
and Go's version resolution keeps them exact as long as nothing requires newer.

```bash
go get github.com/jcsvwinston/nucleus@v1.4.0
go get github.com/jcsvwinston/quark@v1.3.3
go get github.com/jcsvwinston/orbit@v1.4.4
go get github.com/jcsvwinston/orbit/quarkbridge@v0.3.4
go get github.com/jcsvwinston/orbit/quarkdatasource@v0.2.6
```

You also need the SQL drivers your engines use (quark auto-detects the dialect
from the driver name):

```bash
go get github.com/jackc/pgx/v5
go get github.com/go-sql-driver/mysql
```

One rule worth adopting from day one: **never add a `go.work` that points at
suite checkouts**. A workspace silently overrides your pins and you stop
building against what you deploy. This repo runs `GOWORK=off` in CI and fails
the build if a workspace file appears (`scripts/check_no_workspace.sh`).

## 2. Define the domain as quark models

Quark models are plain structs with tags (`internal/warehouse/models.go`):

```go
type Product struct {
    ID         int64     `db:"id" pk:"true" json:"id"`
    SKU        string    `db:"sku" quark:"unique,not_null" json:"sku"`
    Name       string    `db:"name" quark:"not_null" json:"name"`
    PriceCents int64     `db:"price_cents" quark:"not_null" json:"price_cents"`
    Stock      int64     `db:"stock" json:"stock"`
    CreatedAt  time.Time `db:"created_at" json:"created_at"`
}

func (p *Product) BeforeCreate(ctx context.Context) error {
    if p.CreatedAt.IsZero() {
        p.CreatedAt = time.Now().UTC()
    }
    return nil
}
```

Open one client per database. This app keeps products, orders and users on
PostgreSQL and an append-only audit trail on MySQL — same models API, two
engines:

```go
pg, err := quark.New("pgx", pgDSN, quark.WithMaxOpenConns(10))
my, err := quark.New("mysql", mysqlDSN, quark.WithMaxOpenConns(10))
```

Schema comes from the models themselves:

```go
pg.RegisterModel(&Product{}, &Order{}, &AppUser{})
pg.MigrateRegistered(ctx)
my.RegisterModel(&StockMovement{})
my.MigrateRegistered(ctx)
```

Queries are typed through generics:

```go
products, err := quark.For[Product](ctx, pg).OrderBy("id", "DESC").List()
err = quark.For[Product](ctx, pg).Create(&p)
rows, err := quark.For[Product](ctx, pg).Where("id", "=", id).UpdateMap(updates)
```

If you run a PostgreSQL streaming replica, quark can route reads to it —
`quark.New("pgx", primaryDSN, quark.WithReplicas(replicaDSN))` — writes always
go to the primary. This app uses a replica-routed client for its inventory
report (`/api/reports/inventory`); the compose file shows a minimal streaming
replica setup with the official `postgres:16` image.

## 3. Configure nucleus in YAML

Nucleus loads a single YAML file (`config/e2e.yaml` here). The interesting
parts:

```yaml
databases:
  default:
    url: "postgres://warehouse:warehouse@127.0.0.1:55442/warehouse?sslmode=disable"
  audit:
    url: "mysql://warehouse:warehouse@127.0.0.1:53306/warehouse_audit"

redis_url: "redis://127.0.0.1:56379/0"
session_store: redis          # sessions live in Redis, not process memory

mail_driver: smtp
smtp_host: "127.0.0.1"
smtp_port: 51025
mail_from: "warehouse@example.com"

storage:
  provider: s3                # S3-compatible; MinIO in development
  s3:
    endpoint: "http://127.0.0.1:59000"
    bucket: "warehouse"
    use_path_style: true      # required for MinIO
    access_key_id: "minioadmin"
    secret_access_key: "minioadmin"
    region: "us-east-1"

outbox:
  enabled: true               # transactional outbox + background dispatcher
  bridges:
    - name: order-hooks
      type: webhook
      config:
        url: "http://127.0.0.1:58080/hooks/outbox"
        pattern: "orders.*"
        headers:
          X-Outbox-Token: "dev-outbox-token"
```

Every key can be overridden with `NUCLEUS_*` environment variables
(`NUCLEUS_DATABASES__DEFAULT__URL`, `NUCLEUS_PORT`, …), which is how the same
file serves development and CI.

## 4. Write the app as nucleus modules

A nucleus module bundles lifecycle, routes and dependencies
(`internal/warehouse/module.go`):

```go
return nucleus.Module[struct{}]{
    Name: "warehouse",
    OnStart: func(ctx context.Context, rt nucleus.Runtime, _ struct{}) error {
        m.db = rt.DB()            // framework-managed *sql.DB (default database)
        m.sess = rt.Session()     // session manager (Redis-backed per config)
        m.mailer = rt.Mailer()    // SMTP sender per config
        m.store = rt.Storage()    // S3 object store per config
        // ...
        return nil
    },
    Routes: func(r nucleus.Router, _ struct{}) {
        r.Post("/api/login", m.login)
        r.Get("/api/products", m.listProducts)
        r.Post("/api/orders", m.createOrder)
        // ...
    },
}.Build()
```

Handlers take a `*nucleus.Context`:

```go
func (m *module) listProducts(c *nucleus.Context) error {
    products, err := quark.For[Product](c.Request.Context(), m.pg).OrderBy("id", "DESC").List()
    if err != nil {
        return err
    }
    return c.JSON(http.StatusOK, map[string]any{"products": products})
}
```

A module can bind to a non-default database alias — this app's `audit` module
declares `DefaultDB: "audit"` and `Requires: []string{"audit"}`, so its
`rt.DB()` is the managed MySQL pool and boot fails loudly if the alias is
missing from the config.

### Sessions

Login and logout are a few lines against the session manager; with
`session_store: redis` the session data lives in Redis and survives restarts:

```go
m.sess.RenewToken(ctx)                 // defends against session fixation
m.sess.Put(ctx, "user_id", id)         // ... on login
m.sess.Destroy(ctx)                    // ... on logout
```

### Mail and storage

```go
m.mailer.Send(ctx, mail.Message{From: from, To: []string{to}, Subject: s, Body: b})
info, err := m.store.Put(ctx, key, reader, storage.PutOptions{ContentType: ct})
rc, info, err := m.store.Get(ctx, key)
err = m.store.Delete(ctx, key)
```

### Transactional outbox

The order flow needs "write the order AND announce it" to be atomic. The
outbox does that with one SQL transaction on the framework-managed pool
(`internal/warehouse/handlers_orders.go`):

```go
tx, _ := m.db.BeginTx(ctx, nil)
// decrement stock, insert the order as 'pending' ...
m.outbox.EnqueueTx(ctx, tx, outbox.Entry{Topic: "orders.placed", Payload: payload})
tx.Commit()
```

With `outbox.enabled: true`, nucleus runs a background dispatcher that leases
pending events and delivers them through the configured webhook bridge — here,
back to the app's own `/hooks/outbox`, which emails the customer and flips the
order to `confirmed`. Delivery is at-least-once, so the hook is idempotent.

The enqueue handle is a plain `outbox.NewStore(rt.DB(), …)` over the same
table the dispatcher polls (`nucleus_outbox`).

## 5. Mount the orbit admin panel

Two adapters connect orbit to quark, and both are one-liners to wire
(`cmd/quantum-app/main.go`):

```go
// Data Studio browses and edits the quark models.
ds := quarkdatasource.New(pg)
quarkdatasource.Register[warehouse.Product](ds)
quarkdatasource.Register[warehouse.Order](ds)

app, err := nucleus.New().
    FromConfigFile(configPath).
    Mount(warehouse.Module(deps)).
    Mount(orbit.Module(orbit.Config{
        Prefix:            "/admin",
        Title:             "Warehouse Admin",
        DataSource:        ds,
        BootstrapUsername: "admin",
        BootstrapEmail:    "admin@warehouse.local",
        BootstrapPassword: adminPassword, // change beyond a laptop
    })).
    Build()
```

And in the module's `OnStart`, bridge the quark clients to the live feed so
every statement your handlers run shows up in the panel, correlated to the
request that caused it:

```go
bridged, err := pg.WithOptions(quark.WithMiddleware(quarkbridge.New(rt.Observability())))
```

Run the app and open `http://localhost:58080/admin`: log in with the bootstrap
admin, browse Product/Order in Data Studio, and watch the Live view while you
hit the API.

## 6. Run it

```bash
docker compose up -d --wait          # PG (+replica), MySQL, Redis, MinIO, Mailpit
go run ./e2e/setup                   # creates the MinIO bucket
go build -o bin/quantum-app ./cmd/quantum-app
bin/quantum-app --config config/e2e.yaml
```

Try it:

```bash
curl -s -c /tmp/cj -X POST localhost:58080/api/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"ops@warehouse.local","password":"warehouse-ops"}'

curl -s -b /tmp/cj -X POST localhost:58080/api/products \
  -H 'Content-Type: application/json' \
  -d '{"sku":"CRATE-1","name":"Crate","price_cents":1250,"stock":10}'

curl -s -X POST localhost:58080/api/orders \
  -H 'Content-Type: application/json' \
  -d '{"product_id":1,"quantity":2,"customer_email":"you@example.com"}'
```

Within a couple of seconds the order flips to `confirmed` and the confirmation
email lands in Mailpit (`http://localhost:58025`). The admin panel is at
`http://localhost:58080/admin`.

The full end-to-end suite that exercises all of the above against the real
services is `./scripts/e2e_local.sh` — the same thing CI runs on every push.
