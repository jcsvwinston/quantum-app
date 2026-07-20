#!/usr/bin/env bash
# Gate for suite-manifest.yaml (runs in CI): exit 1 when any denominator item
# is unclassified, when a classification is orphaned (its item or pattern no
# longer exists in the pinned inventories), when a covered entry lacks
# evidence / a not-covered or out-of-scope entry lacks a reason, or when the
# manifest pins drift from go.mod.
set -euo pipefail
cd "$(dirname "$0")/.."
GOWORK=off exec go run ./tools/suitemanifest -mode check "$@"
