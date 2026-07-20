#!/usr/bin/env bash
# Full local E2E: docker compose up, run the suite against the real binary,
# compose down. Mirrors what CI does with job services.
set -euo pipefail
cd "$(dirname "$0")/.."

docker compose up -d --wait
trap 'docker compose down -v' EXIT

GOWORK=off go build -o bin/quantum-app ./cmd/quantum-app
./scripts/e2e_run.sh
