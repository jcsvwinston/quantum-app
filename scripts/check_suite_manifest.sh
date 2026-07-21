#!/usr/bin/env bash
# Gate for suite-manifest.yaml (runs in CI): exit 1 when any denominator item
# is unclassified, when a classification is orphaned (its item or pattern no
# longer exists in the pinned inventories), when a covered entry lacks
# evidence / a not-covered or out-of-scope entry lacks a reason, or when the
# manifest pins drift from go.mod.
#
# It also runs the human-labels gate first (scripts/check_human_labels.sh):
# the pins↔go.mod half is mechanical, but README/TUTORIAL/go.mod-comment/
# suite: are read by humans and fossilized on the last set bump — both halves
# must hold for the manifest step to be green.
set -euo pipefail
cd "$(dirname "$0")/.."
./scripts/check_human_labels.sh
GOWORK=off exec go run ./tools/suitemanifest -mode check "$@"
