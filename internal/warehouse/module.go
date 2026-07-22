package warehouse

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/jcsvwinston/nucleus/pkg/auth"
	"github.com/jcsvwinston/nucleus/pkg/mail"
	"github.com/jcsvwinston/nucleus/pkg/nucleus"
	"github.com/jcsvwinston/nucleus/pkg/outbox"
	"github.com/jcsvwinston/nucleus/pkg/storage"
	"github.com/jcsvwinston/orbit/quarkbridge"
	"github.com/jcsvwinston/quark"
)

// Deps carries the Quark clients and app-level settings main wires up.
type Deps struct {
	// PG is the primary PostgreSQL client: writes, reads that must be
	// strongly consistent, and the orbit Data Studio datasource.
	PG *quark.Client
	// PGRead routes reads through the PostgreSQL streaming replica
	// (quark.WithReplicas). May equal PG when no replica is configured.
	PGRead *quark.Client
	// MySQL is the client for the aliased audit database.
	MySQL *quark.Client

	// OutboxSecret is the shared HMAC secret for the outbox webhook bridge:
	// /hooks/outbox verifies the bridge's X-Nucleus-Signature body signature
	// against it (the same value the nucleus config sets as the bridge's
	// config.secret). It is a required deployment secret — main wires it from
	// WAREHOUSE_OUTBOX_SECRET through mustEnv, so it is never empty here.
	OutboxSecret string
	// MailFrom is the sender address for outbound mail (mirrors the
	// mail_from config key, which modules cannot read back from the Runtime).
	MailFrom string
}

// module holds the runtime-bound state captured in OnStart.
type module struct {
	deps Deps

	db     *sql.DB
	sess   *auth.SessionManager
	mailer mail.Sender
	store  storage.Store
	log    *slog.Logger
	outbox *outbox.Store

	// bridgedPG / bridgedMy wrap every statement with orbit/quarkbridge, so
	// the SQL the HTTP handlers run shows up in the orbit live feed
	// correlated to the request.
	bridgedPG *quark.Client
	bridgedMy *quark.Client
}

// Module returns the warehouse feature as a nucleus module.
func Module(deps Deps) nucleus.ModuleSpec {
	m := &module{deps: deps}

	return nucleus.Module[struct{}]{
		Name: "warehouse",

		OnStart: func(ctx context.Context, rt nucleus.Runtime, _ struct{}) error {
			m.db = rt.DB()
			if m.db == nil {
				return fmt.Errorf("warehouse: no default database configured")
			}
			m.sess = rt.Session()
			m.mailer = rt.Mailer()
			m.store = rt.Storage()
			m.log = rt.Logger()

			// Bridge both Quark clients to the observability bus so their SQL
			// reaches the orbit live feed (quarkbridge derives a new client;
			// the originals stay unbridged for Data Studio and migrations).
			bridge := quarkbridge.New(rt.Observability())
			bridgedPG, err := m.deps.PG.WithOptions(quark.WithMiddleware(bridge))
			if err != nil {
				return fmt.Errorf("warehouse: derive bridged PG client: %w", err)
			}
			m.bridgedPG = bridgedPG
			bridgedMy, err := m.deps.MySQL.WithOptions(quark.WithMiddleware(bridge))
			if err != nil {
				return fmt.Errorf("warehouse: derive bridged MySQL client: %w", err)
			}
			m.bridgedMy = bridgedMy

			// Outbox enqueue handle over the framework-managed default pool and
			// the same table the framework's dispatcher polls (outbox.enabled
			// in the config): EnqueueTx joins app writes and events in one
			// transaction; the framework relay delivers them.
			ob, err := outbox.NewStore(m.db, outbox.Config{
				TableName: outbox.DefaultTableName,
				Flavor:    outbox.FlavorPostgres,
			})
			if err != nil {
				return fmt.Errorf("warehouse: outbox store: %w", err)
			}
			m.outbox = ob

			m.log.Info("warehouse: module ready",
				"pg_replicas", m.deps.PG != m.deps.PGRead,
				"outbox_table", outbox.DefaultTableName)
			return nil
		},

		Routes: func(r nucleus.Router, _ struct{}) {
			// Session-backed auth (Redis-backed session store per config).
			r.Post("/api/login", m.login)
			r.Post("/api/logout", m.logout)
			r.Get("/api/me", m.me)

			// Product CRUD (Quark on PostgreSQL; writes audit to MySQL).
			r.Get("/api/products", m.listProducts)
			r.Post("/api/products", m.createProduct)
			r.Get("/api/products/{id}", m.getProduct)
			r.Put("/api/products/{id}", m.updateProduct)
			r.Delete("/api/products/{id}", m.deleteProduct)

			// Product datasheet (object storage: S3/MinIO per config).
			r.Put("/api/products/{id}/datasheet", m.putDatasheet)
			r.Get("/api/products/{id}/datasheet", m.getDatasheet)
			r.Delete("/api/products/{id}/datasheet", m.deleteDatasheet)

			// Orders: transactional outbox on the write, webhook relay flips
			// them to confirmed and emails the customer.
			r.Post("/api/orders", m.createOrder)
			r.Get("/api/orders", m.listOrders)
			r.Get("/api/orders/{id}", m.getOrder)

			// Stock movements (Quark on the aliased MySQL database).
			r.Get("/api/stock-movements", m.listStockMovements)

			// Inventory report reads through the PostgreSQL replica.
			r.Get("/api/reports/inventory", m.inventoryReport)

			// Outbox delivery target (called by the framework's webhook bridge).
			r.Post("/hooks/outbox", m.outboxHook)
		},
	}.Build()
}

// AuditModule is a second module bound to the "audit" database alias
// (Module.DefaultDB): its rt.DB() is the framework-managed MySQL pool, so the
// databases.<alias> surface is exercised end to end, and Requires makes boot
// fail loud if the alias is missing from the config.
func AuditModule() nucleus.ModuleSpec {
	var db *sql.DB

	return nucleus.Module[struct{}]{
		Name:      "audit",
		DefaultDB: "audit",
		Requires:  []string{"audit"},

		OnStart: func(ctx context.Context, rt nucleus.Runtime, _ struct{}) error {
			db = rt.DB()
			if db == nil {
				return fmt.Errorf("audit: no database handle for alias %q", "audit")
			}
			return db.PingContext(ctx)
		},

		Routes: func(r nucleus.Router, _ struct{}) {
			r.Get("/api/audit/health", func(c *nucleus.Context) error {
				var n int64
				err := db.QueryRowContext(c.Request.Context(),
					"SELECT COUNT(*) FROM stock_movements").Scan(&n)
				if err != nil {
					return err
				}
				return c.JSON(200, map[string]any{
					"database":        "audit",
					"engine":          "mysql",
					"stock_movements": n,
				})
			})
		},
	}.Build()
}

// Migrate registers and migrates the Quark models on both engines and seeds
// the operations login. Called from main before the server starts.
func Migrate(ctx context.Context, pg, my *quark.Client, opsEmail, opsPassword string) error {
	if err := pg.RegisterModel(&Product{}, &Order{}, &AppUser{}); err != nil {
		return fmt.Errorf("warehouse: register PG models: %w", err)
	}
	if err := pg.MigrateRegistered(ctx); err != nil {
		return fmt.Errorf("warehouse: migrate PG models: %w", err)
	}
	if err := my.RegisterModel(&StockMovement{}); err != nil {
		return fmt.Errorf("warehouse: register MySQL models: %w", err)
	}
	if err := my.MigrateRegistered(ctx); err != nil {
		return fmt.Errorf("warehouse: migrate MySQL models: %w", err)
	}

	n, err := quark.For[AppUser](ctx, pg).Where("email", "=", opsEmail).Count()
	if err != nil {
		return fmt.Errorf("warehouse: count users: %w", err)
	}
	if n == 0 {
		hash, err := auth.HashPassword(opsPassword)
		if err != nil {
			return fmt.Errorf("warehouse: hash ops password: %w", err)
		}
		u := AppUser{Email: opsEmail, PasswordHash: hash, Name: "Warehouse Ops"}
		if err := quark.For[AppUser](ctx, pg).Create(&u); err != nil {
			return fmt.Errorf("warehouse: seed ops user: %w", err)
		}
	}
	return nil
}
