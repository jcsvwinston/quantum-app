#!/usr/bin/env bash
# Prints the suite-manifest DENOMINATOR: every item of the inventories the
# suite repos publish, read from the module cache at the exact tags pinned in
# go.mod. With a suite-manifest.yaml present it also lists (on stderr) the
# items the manifest leaves unclassified — the authoring aid for the gate.
set -euo pipefail
cd "$(dirname "$0")/.."
GOWORK=off exec go run ./tools/suitemanifest -mode gen "$@"
