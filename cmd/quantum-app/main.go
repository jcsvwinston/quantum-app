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
	"fmt"
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

// envOr reads an environment variable with a fallback. Use it only for
// NON-SECRET deployment settings that have a safe, non-privileged default —
// localhost DSNs for development, sender addresses, panel labels. Anything
// that grants access (see mustEnv) must never have a default baked into the
// source tree.
func envOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}

// exampleSecretValues are the placeholder credential values that appear in
// this repository's example config and scripts. They are public by definition
// (anyone can read them here), so a real deployment that still carries one is
// not configured — it is exposed. mustEnv rejects them explicitly so copying
// an example .env into production fails loud instead of silently shipping a
// known credential.
var exampleSecretValues = map[string]string{
	"dev-outbox-secret": "the example outbox HMAC secret from config/e2e.yaml",
	"dev-outbox-token":  "the example (now removed) legacy outbox token",
	"warehouse-ops":     "the example ops password from the tutorial",
}

// validateDeploymentSecret checks one required deployment secret. It returns a
// non-nil error explaining why the value is unacceptable — unset, empty, or a
// public example value from the repo — and nil when the value is safe to use.
// Kept separate from mustEnv so it is unit-testable without exiting the
// process.
func validateDeploymentSecret(key, value string, present bool) error {
	if !present || value == "" {
		return fmt.Errorf("%s must be set to a deployment secret: it grants access and has no safe default (a default in the source tree would be a public credential); refusing to start", key)
	}
	if what, ok := exampleSecretValues[value]; ok {
		return fmt.Errorf("%s is set to %q — %s, not a secret. Generate your own value; refusing to start", key, value, what)
	}
	return nil
}

// mustEnv returns the value of a required deployment secret, aborting the
// process with a clear message when the variable is unset, empty, or still
// holds a public example value. Fail-closed: a misconfigured secret must stop
// startup, never fall back to a default that lives in the repository.
func mustEnv(key string) string {
	v, ok := os.LookupEnv(key)
	if err := validateDeploymentSecret(key, v, ok); err != nil {
		log.Fatalf("quantum-app: %v", err)
	}
	return v
}

func main() {
	configPath := flag.String("config", "nucleus.yml", "path to the nucleus config file")
	flag.Parse()
	ctx := context.Background()

	// Deployment secrets are fail-closed: they grant access and have no safe
	// default, so validate them before opening any connection. A misconfigured
	// deploy dies here — immediately and loud — rather than booting on a
	// credential that is public in the source tree. See mustEnv; the
	// secret/non-secret boundary is documented in README.md and docs/TUTORIAL.md.
	outboxSecret := mustEnv("WAREHOUSE_OUTBOX_SECRET")
	opsPassword := mustEnv("WAREHOUSE_OPS_PASSWORD")

	// The encoding the /hooks/outbox consumer expects — it MUST match the
	// bridge's `payload_encoding` in the nucleus config (this app configures
	// `json`). Not a secret, so it has a default; the consumer decodes by this
	// value, never by the unsigned request header (SEC-3).
	outboxEncoding := envOr("WAREHOUSE_OUTBOX_ENCODING", "json")
	switch outboxEncoding {
	case "json", "base64":
	default:
		log.Fatalf("quantum-app: WAREHOUSE_OUTBOX_ENCODING must be \"json\" or \"base64\", got %q", outboxEncoding)
	}

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
			PG:             pg,
			PGRead:         pgRead,
			MySQL:          my,
			OutboxSecret:   outboxSecret,
			OutboxEncoding: outboxEncoding,
			MailFrom:       envOr("WAREHOUSE_MAIL_FROM", "warehouse@quantum-app.local"),
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
