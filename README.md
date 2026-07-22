# quantum-app

[![CI](https://github.com/jcsvwinston/quantum-app/actions/workflows/ci.yml/badge.svg)](https://github.com/jcsvwinston/quantum-app/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

**A real, end-to-end reference application for the [Quantum](https://jcsvwinston.github.io/quantum/) suite** — a small
warehouse (inventory & orders) service built the way an external team would build it:
[nucleus](https://github.com/jcsvwinston/nucleus) as the web framework,
[quark](https://github.com/jcsvwinston/quark) models for the data layer, and the
[orbit](https://github.com/jcsvwinston/orbit) admin panel mounted in-process.

This is not a demo folder inside the suite. It is a standalone consumer that resolves
every suite module **from the Go module proxy at exact released tags** — no workspace,
no replace directives — and runs the whole thing against real infrastructure
(PostgreSQL, MySQL, Redis, MinIO, SMTP) in Docker, in CI, on every push.

## Version policy

The dependency pins in `go.mod` always match a **certified Quantum set** (see the
suite's `versions.yaml`). They are bumped only when a new set is certified — never
ad hoc, and never to intermediate module releases. Builds and CI run with
`GOWORK=off`; a CI guard fails the build if a `go.work` file ever appears in the tree.

Current set: **Quantum 1.9.0** (quark v1.4.0 · nucleus v1.5.0 · orbit v1.5.0).

## What it exercises

- HTTP API for products and orders (nucleus router + modules)
- Quark models on two engines: PostgreSQL (primary) and MySQL (aliased secondary)
- Read routing through a real PostgreSQL streaming replica (quark `WithReplicas`)
- Sessions in Redis, S3 object storage against MinIO, order-confirmation email over SMTP
- Transactional outbox on PostgreSQL: one transaction writes the order and the
  event; the framework's leasing dispatcher delivers it back through a webhook bridge
- The orbit admin panel over real quark models — Data Studio CRUD and the live
  SQL feed via orbit/quarkbridge
- `suite-manifest.yaml`: an honest, CI-gated map of which certified-suite surfaces
  this app executes, which it doesn't, and why

## Deployment secrets (fail-closed)

Credentials have **no defaults baked into the source tree** — a default secret
in a public repo is a public secret. The app is fail-closed: it refuses to
start (a clear `log.Fatalf`, non-zero exit) if a required deployment secret is
unset, empty, or still set to one of the repo's `dev-`/example placeholder
values. This is deliberate for a reference app others copy: copying the example
config into production must fail loud, not ship a known credential.

| Variable | Kind | Required? | Notes |
|---|---|---|---|
| `WAREHOUSE_OUTBOX_SECRET` | **deployment secret** | **always** | HMAC secret the outbox webhook bridge signs with and `/hooks/outbox` verifies. No default; rejects `dev-outbox-secret`. |
| `WAREHOUSE_OPS_PASSWORD` | **deployment secret** | **always** | Seeds the operations login. No default; rejects `warehouse-ops`. |
| `WAREHOUSE_PG_DSN` / `WAREHOUSE_PG_REPLICA_DSN` / `WAREHOUSE_MYSQL_DSN` | non-secret dev setting | no | Default to **localhost** DSNs for local dev only; a real deployment overrides them. A localhost URL is not itself a production secret, so these stay `envOr`. |
| `WAREHOUSE_MAIL_FROM`, `WAREHOUSE_OPS_EMAIL`, `WAREHOUSE_ADMIN_USER`, `WAREHOUSE_ADMIN_EMAIL` | non-secret label | no | Addresses/usernames, not credentials. |
| `WAREHOUSE_ADMIN_PASSWORD` | credential (out of scope here) | no | Still `envOr` with a placeholder default; see the security note below. |

The boundary in one line: **deployment secrets (`WAREHOUSE_OUTBOX_SECRET`,
`WAREHOUSE_OPS_PASSWORD`) are mandatory in any non-CI deployment**; localhost
DSNs and non-credential labels may keep their dev defaults. CI/E2E export
clearly-CI values (e.g. `ci-e2e-outbox-secret`), never a `dev-` one.

> Security note: `WAREHOUSE_ADMIN_PASSWORD` (the orbit panel bootstrap password)
> is also a credential-with-default and should be fail-closed the same way; it
> is left as `envOr` here because it is outside this change's scope.

## Getting started

See [`docs/TUTORIAL.md`](docs/TUTORIAL.md) for the full walkthrough — from
`go mod init` to a running app with an admin panel. To run everything locally:

```bash
docker compose up -d --wait   # PG (+streaming replica), MySQL, Redis, MinIO, Mailpit
./scripts/e2e_run.sh          # builds the binary, boots it, runs the E2E suite
```

`scripts/e2e_run.sh` exports the required secrets with clearly-CI values, so the
fail-closed startup passes; running the binary by hand needs them set (see the
tutorial).

## License

MIT — see [LICENSE](LICENSE).
