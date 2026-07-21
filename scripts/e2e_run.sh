#!/usr/bin/env bash
# Runs the E2E suite against the REAL app binary and real backing services.
# Assumes the services are already up (docker compose up -d --wait; CI runs
# the same). Builds the binary if missing, prepares external state (MinIO
# bucket), boots the app with the production-like YAML config, waits for
# readiness, runs `go test ./e2e`, and always tears the app process down.
set -euo pipefail
cd "$(dirname "$0")/.."

export GOWORK=off

# Service endpoints (host-mapped ports; identical in CI and docker compose).
export QA_PG_DSN="${QA_PG_DSN:-postgres://warehouse:warehouse@127.0.0.1:55442/warehouse?sslmode=disable}"
export QA_PG_REPLICA_DSN="${QA_PG_REPLICA_DSN:-postgres://warehouse:warehouse@127.0.0.1:55443/warehouse?sslmode=disable}"
export QA_MYSQL_DSN="${QA_MYSQL_DSN:-warehouse:warehouse@tcp(127.0.0.1:53306)/warehouse_audit?parseTime=true}"
export QA_REDIS_ADDR="${QA_REDIS_ADDR:-127.0.0.1:56379}"
export QA_S3_ENDPOINT="${QA_S3_ENDPOINT:-http://127.0.0.1:59000}"
export QA_MAILPIT_API="${QA_MAILPIT_API:-http://127.0.0.1:58025}"
export QA_APP_URL="${QA_APP_URL:-http://127.0.0.1:58080}"
export QA_OPS_EMAIL="${QA_OPS_EMAIL:-ops@warehouse.local}"
export QA_OPS_PASSWORD="${QA_OPS_PASSWORD:-warehouse-ops}"
export QA_ADMIN_USER="${QA_ADMIN_USER:-admin}"
export QA_ADMIN_PASSWORD="${QA_ADMIN_PASSWORD:-warehouse-admin}"
# Outbox webhook authentication. QA_OUTBOX_SECRET must match the bridges'
# config.secret in config/e2e.yaml (HMAC body signature); QA_OUTBOX_TOKEN is
# the legacy static header the pinned nucleus v1.4.0 still sends.
export QA_OUTBOX_SECRET="${QA_OUTBOX_SECRET:-dev-outbox-secret}"
export QA_OUTBOX_TOKEN="${QA_OUTBOX_TOKEN:-dev-outbox-token}"

# The app reads the same values through its own env keys.
export WAREHOUSE_PG_DSN="$QA_PG_DSN"
export WAREHOUSE_PG_REPLICA_DSN="$QA_PG_REPLICA_DSN"
export WAREHOUSE_MYSQL_DSN="$QA_MYSQL_DSN"
export WAREHOUSE_OPS_EMAIL="$QA_OPS_EMAIL"
export WAREHOUSE_OPS_PASSWORD="$QA_OPS_PASSWORD"
export WAREHOUSE_ADMIN_USER="$QA_ADMIN_USER"
export WAREHOUSE_ADMIN_PASSWORD="$QA_ADMIN_PASSWORD"
export WAREHOUSE_OUTBOX_SECRET="$QA_OUTBOX_SECRET"
export WAREHOUSE_OUTBOX_TOKEN="$QA_OUTBOX_TOKEN"

echo "==> building bin/quantum-app"
go build -o bin/quantum-app ./cmd/quantum-app

echo "==> preparing external state (MinIO bucket)"
go run ./e2e/setup

echo "==> starting quantum-app against real services"
APP_LOG="$(mktemp "${TMPDIR:-/tmp}/quantum-app-e2e.XXXXXX")"
bin/quantum-app --config config/e2e.yaml >"$APP_LOG" 2>&1 &
APP_PID=$!
trap 'kill "$APP_PID" 2>/dev/null || true; wait "$APP_PID" 2>/dev/null || true' EXIT

echo "==> waiting for the app to become ready"
ready=0
for _ in $(seq 1 60); do
  if curl -fsS "$QA_APP_URL/healthz" >/dev/null 2>&1; then
    ready=1
    break
  fi
  if ! kill -0 "$APP_PID" 2>/dev/null; then
    echo "FAIL: app process exited during startup; log follows" >&2
    cat "$APP_LOG" >&2
    exit 1
  fi
  sleep 1
done
if [ "$ready" != "1" ]; then
  echo "FAIL: app did not become ready in 60s; log follows" >&2
  cat "$APP_LOG" >&2
  exit 1
fi

echo "==> running E2E suite"
if ! go test -count=1 -v ./e2e; then
  echo "==== app log ====" >&2
  cat "$APP_LOG" >&2
  exit 1
fi
echo "==> E2E green"
