#!/usr/bin/env bash
# Guard: quantum-app must resolve the Quantum suite exclusively through the Go
# module proxy at the certified tags pinned in go.mod. A workspace file would
# silently override those pins with local checkouts, so its mere presence is a
# failure — as is a Go environment where GOWORK resolves to anything at all.
#
# Usage: scripts/check_no_workspace.sh   (exit 0 = clean, exit 1 = violation)
set -u

fail=0

# 1. No go.work anywhere in the tree (tracked or untracked).
found="$(find . -name 'go.work' -not -path './.git/*' 2>/dev/null)"
if [ -n "$found" ]; then
  echo "FAIL: workspace file(s) present in the tree:" >&2
  echo "$found" >&2
  fail=1
fi

# 2. `go env GOWORK` must be empty: neither a go.work discovered upward from
#    this directory nor a GOWORK env var pointing at one elsewhere.
gowork="$(cd "$(dirname "$0")/.." && go env GOWORK)"
if [ -n "$gowork" ] && [ "$gowork" != "off" ]; then
  echo "FAIL: go env GOWORK resolves to '$gowork' (must be empty or 'off')" >&2
  fail=1
fi

if [ "$fail" -ne 0 ]; then
  exit 1
fi
echo "OK: no workspace in play — suite modules resolve from the module proxy at the pinned tags"
