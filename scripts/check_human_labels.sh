#!/usr/bin/env bash
# Gate for the HUMAN-readable set labels (runs inside the suite-manifest gate
# step): every version a human-facing surface names must match what go.mod
# actually pins. The machine side is already gated (tools/suitemanifest fails
# on manifest-pins↔go.mod drift); this closes the human side, which fossilized
# on the last set bump: README ("Current set"), docs/TUTORIAL.md (the
# `go get …@vX` lines), the go.mod set comment and the manifest `suite:` label
# kept naming the previous set while the pins moved on.
#
# Dynamic by construction: the truth is read from go.mod on every run (the
# require block for module versions, its header comment for the set number).
# Nothing here hardcodes a version, so the gate works unchanged in any set.
#
# Usage: scripts/check_human_labels.sh   (exit 0 = labels match, 1 = fossil)
set -euo pipefail
cd "$(dirname "$0")/.."

fail=0
problem() { echo "FAIL: $*" >&2; fail=1; }

# ---- truth: the go.mod require block ---------------------------------------
# pin_of <module-path-under-jcsvwinston> -> version go.mod pins (empty if none)
pin_of() {
  awk -v m="github.com/jcsvwinston/$1" \
    '$1 == m && $2 ~ /^v[0-9]/ { print $2; exit }' go.mod
}

# short suite-module names a human label may use -> module path
path_of() {
  case "$1" in
    quark)           echo "quark" ;;
    nucleus)         echo "nucleus" ;;
    orbit)           echo "orbit" ;;
    quarkbridge)     echo "orbit/quarkbridge" ;;
    quarkdatasource) echo "orbit/quarkdatasource" ;;
    *)               echo "" ;;
  esac
}

# ---- truth: the set number go.mod's own comment names ----------------------
SET="$(sed -n 's/.*Quantum certified set \([0-9][0-9.]*\)[^0-9.].*/\1/p' go.mod | head -1)"
if [ -z "$SET" ]; then
  problem "go.mod names no certified set (expected a 'Quantum certified set X.Y.Z' comment)"
fi

# ---- check 1: every '<module> vX.Y.Z' prose mention (README, TUTORIAL) -----
prose_mentions=0
for f in README.md docs/TUTORIAL.md; do
  while read -r name ver; do
    [ -n "$name" ] || continue
    prose_mentions=$((prose_mentions + 1))
    mod="$(path_of "$name")"
    pin="$(pin_of "$mod")"
    if [ -z "$pin" ]; then
      problem "$f names '$name $ver' but go.mod has no pin for github.com/jcsvwinston/$mod"
    elif [ "$ver" != "$pin" ]; then
      problem "$f names '$name $ver' but go.mod pins $pin — fossil label"
    fi
  done < <(grep -ohE '\b(quark|nucleus|orbit|quarkbridge|quarkdatasource) v[0-9]+\.[0-9]+\.[0-9]+' "$f" \
             | sed 's/ / /' | awk '{print $1, $2}')
done

# ---- check 2: every 'github.com/jcsvwinston/<mod>@vX' mention --------------
at_mentions=0
for f in README.md docs/TUTORIAL.md; do
  while read -r path ver; do
    [ -n "$path" ] || continue
    at_mentions=$((at_mentions + 1))
    pin="$(pin_of "$path")"
    if [ -z "$pin" ]; then
      problem "$f names 'github.com/jcsvwinston/$path@$ver' but go.mod has no pin for that module"
    elif [ "$ver" != "$pin" ]; then
      problem "$f names 'github.com/jcsvwinston/$path@$ver' but go.mod pins $pin — fossil label"
    fi
  done < <(grep -ohE 'github\.com/jcsvwinston/[a-z0-9/]+@v[0-9]+\.[0-9]+\.[0-9]+' "$f" \
             | sed -E 's#github\.com/jcsvwinston/([a-z0-9/]+)@#\1 #')
done

# The gate must never pass by matching nothing: README carries the Current-set
# line and the TUTORIAL carries the go get lines. Zero mentions means the
# patterns (or the docs) fossilized — fail loud instead of going silently dead.
if [ "$prose_mentions" -eq 0 ]; then
  problem "no '<module> vX.Y.Z' labels found in README.md/docs/TUTORIAL.md — gate pattern or docs fossilized"
fi
if [ "$at_mentions" -eq 0 ]; then
  problem "no 'github.com/jcsvwinston/<mod>@vX.Y.Z' labels found in docs/TUTORIAL.md — gate pattern or docs fossilized"
fi

# ---- check 3: the set number on every human surface ------------------------
if [ -n "$SET" ]; then
  # README's "Current set" line must exist and name the go.mod set.
  readme_line="$(grep -m1 'Current set' README.md || true)"
  if [ -z "$readme_line" ]; then
    problem "README.md has no 'Current set' line"
  else
    readme_set="$(printf '%s\n' "$readme_line" | sed -n 's/.*Quantum \([0-9][0-9.]*\)[^0-9.].*/\1/p')"
    if [ "$readme_set" != "$SET" ]; then
      problem "README.md 'Current set' names Quantum ${readme_set:-?} but go.mod says certified set $SET"
    fi
  fi

  # The manifest's suite: label must name the same set.
  manifest_set="$(sed -n 's/^suite: *"\([0-9.]*\)".*/\1/p' suite-manifest.yaml | head -1)"
  if [ "$manifest_set" != "$SET" ]; then
    problem "suite-manifest.yaml suite: \"${manifest_set:-?}\" but go.mod says certified set $SET"
  fi
fi

if [ "$fail" -ne 0 ]; then
  exit 1
fi
echo "OK: human labels match go.mod — set $SET, $prose_mentions prose + $at_mentions go-get version label(s) verified"
