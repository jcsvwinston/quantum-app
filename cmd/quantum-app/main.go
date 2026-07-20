// Command quantum-app is a warehouse (inventory & orders) service built on
// the Quantum suite as an external consumer: nucleus hosts the app, quark
// owns the domain models on PostgreSQL and MySQL, and the orbit admin panel
// mounts in-process at /admin with Data Studio backed by the quark models and
// the live SQL feed wired through orbit/quarkbridge.
//
// Every suite dependency resolves from the Go module proxy at the exact tags
// of the certified Quantum set pinned in go.mod — no workspace, no replaces.
package main

import (
	"context"
	"flag"
	"log"
	"os"

	"github.com/jcsvwinston/nucleus/pkg/nucleus"
	"github.com/jcsvwinston/orbit"
	"github.com/jcsvwinston/orbit/quarkdatasource"
	"github.com/jcsvwinston/quark"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/jcsvwinston/quantum-app/internal/warehouse"
)

func envOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}

func main() {
	configPath := flag.String("config", "nucleus.yml", "path to the nucleus config file")
	flag.Parse()
	ctx := context.Background()

	// Quark clients own the domain schema. The DSNs mirror the databases.*
	// URLs in the nucleus config (nucleus manages its own pools for sessions,
	// outbox, and the admin; quark manages the domain pools).
	pgDSN := envOr("WAREHOUSE_PG_DSN",
		"postgres://warehouse:warehouse@127.0.0.1:55442/warehouse?sslmode=disable")
	pgReplicaDSN := envOr("WAREHOUSE_PG_REPLICA_DSN",
		"postgres://warehouse:warehouse@127.0.0.1:55443/warehouse?sslmode=disable")
	myDSN := envOr("WAREHOUSE_MYSQL_DSN",
		"warehouse:warehouse@tcp(127.0.0.1:53306)/warehouse_audit?parseTime=true")

	pg, err := quark.New("pgx", pgDSN, quark.WithMaxOpenConns(10))
	if err != nil {
		log.Fatalf("quantum-app: quark PostgreSQL client: %v", err)
	}
	defer pg.Close()

	// Read path: with a replica DSN configured, reads on this client route to
	// the PostgreSQL streaming replica (quark.WithReplicas, ADR-0015 in
	// quark). Set WAREHOUSE_PG_REPLICA_DSN="" to run without a replica.
	pgRead := pg
	if pgReplicaDSN != "" {
		pgRead, err = quark.New("pgx", pgDSN,
			quark.WithMaxOpenConns(10),
			quark.WithReplicas(pgReplicaDSN))
		if err != nil {
			log.Fatalf("quantum-app: quark replica-routed client: %v", err)
		}
		defer pgRead.Close()
	}

	my, err := quark.New("mysql", myDSN, quark.WithMaxOpenConns(10))
	if err != nil {
		log.Fatalf("quantum-app: quark MySQL client: %v", err)
	}
	defer my.Close()

	opsEmail := envOr("WAREHOUSE_OPS_EMAIL", "ops@warehouse.local")
	opsPassword := envOr("WAREHOUSE_OPS_PASSWORD", "warehouse-ops")
	if err := warehouse.Migrate(ctx, pg, my, opsEmail, opsPassword); err != nil {
		log.Fatalf("quantum-app: migrate: %v", err)
	}

	// Data Studio speaks orbit's datasource contract; back it with the quark
	// models on the primary client.
	ds := quarkdatasource.New(pg)
	if err := quarkdatasource.Register[warehouse.Product](ds); err != nil {
		log.Fatalf("quantum-app: register Product in Data Studio: %v", err)
	}
	if err := quarkdatasource.Register[warehouse.Order](ds); err != nil {
		log.Fatalf("quantum-app: register Order in Data Studio: %v", err)
	}

	app, err := nucleus.New().
		FromConfigFile(*configPath).
		Mount(warehouse.Module(warehouse.Deps{
			PG:          pg,
			PGRead:      pgRead,
			MySQL:       my,
			OutboxToken: envOr("WAREHOUSE_OUTBOX_TOKEN", "dev-outbox-token"),
			MailFrom:    envOr("WAREHOUSE_MAIL_FROM", "warehouse@quantum-app.local"),
		})).
		Mount(warehouse.AuditModule()).
		Mount(orbit.Module(orbit.Config{
			Prefix:            "/admin",
			Title:             "Warehouse Admin",
			DataSource:        ds,
			BootstrapUsername: envOr("WAREHOUSE_ADMIN_USER", "admin"),
			BootstrapEmail:    envOr("WAREHOUSE_ADMIN_EMAIL", "admin@warehouse.local"),
			BootstrapPassword: envOr("WAREHOUSE_ADMIN_PASSWORD", "warehouse-admin"),
		})).
		Build()
	if err != nil {
		log.Fatalf("quantum-app: %v", err)
	}

	// The public API authenticates with its own session checks; skip the
	// framework's default-deny RBAC. Orbit still enforces its session auth
	// under /admin.
	app.Options = append(app.Options, nucleus.WithOpenAuthz())

	if err := nucleus.Run(app); err != nil {
		log.Fatalf("quantum-app: %v", err)
	}
}
