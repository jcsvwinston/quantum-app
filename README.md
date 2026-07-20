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

Current set: **Quantum 1.7.2** (quark v1.3.2 · nucleus v1.3.3 · orbit v1.4.3).

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

## Getting started

See [`docs/TUTORIAL.md`](docs/TUTORIAL.md) for the full walkthrough — from
`go mod init` to a running app with an admin panel. To run everything locally:

```bash
docker compose up -d --wait   # PG (+streaming replica), MySQL, Redis, MinIO, Mailpit
./scripts/e2e_run.sh          # builds the binary, boots it, runs the E2E suite
```

## License

MIT — see [LICENSE](LICENSE).
