#!/usr/bin/env bash
# guard_fixtures.sh — executes the NEGATIVES of every repo gate as fixtures
# (guard-of-guards style, like the umbrella repo): a gate that always passes
# is indistinguishable from a dead gate, so each fixture doctors a temporary
# copy of the tree with one concrete breakage and asserts the REAL gate
# script BITES on it:
#
#   - the gate must exit != 0 on the doctored copy — if it survives
#     (exit 0), the gate is dead and this harness FAILS;
#   - its output must contain the fixture's expected cause — dying for any
#     OTHER reason (e.g. a setup error) is not proof of biting.
#
# Anti-fossil coverage: every scripts/check_*.sh must have at least one
# fixture here; a new gate without its negative fails the harness.
#
# Usage: scripts/guard_fixtures.sh   (CI runs it after the positive gates)
set -uo pipefail
cd "$(dirname "$0")/.."
ROOT="$(pwd)"

export GOWORK=off

WORK="$(mktemp -d "${TMPDIR:-/tmp}/quantum-app-guard-fixtures.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT

overall=0
total=0
bitten=0

# copy_tree <dst>: the minimal doctored-tree skeleton every gate needs — the
# gate scripts themselves plus the files they validate. go.sum/tools go along
# so the suite-manifest gate can run its real `go run` from the copy.
copy_tree() {
  local dst="$1"
  mkdir -p "$dst/docs"
  cp go.mod go.sum suite-manifest.yaml README.md "$dst/"
  cp docs/TUTORIAL.md "$dst/docs/"
  cp -R scripts "$dst/scripts"
  cp -R tools "$dst/tools"
}

# run_fixture <name> <gate-relative-cmd> <expect-regex> <doctor-fn>
run_fixture() {
  local name="$1" gate="$2" expect="$3" doctor="$4"
  total=$((total + 1))
  echo "== $name =="

  local tree="$WORK/$name"
  copy_tree "$tree"
  if ! "$doctor" "$tree"; then
    echo "FAIL: fixture '$name' failed to prepare its doctored tree" >&2
    overall=1
    echo
    return
  fi

  local out="$WORK/$name.out"
  ( cd "$tree" && export GOWORK=off && eval "$gate" ) >"$out" 2>&1
  local ec=$?

  if [ "$ec" -eq 0 ]; then
    echo "FAIL: gate '$gate' SURVIVED fixture '$name' (exit 0) — the breakage it must catch no longer kills it:" >&2
    sed 's/^/    /' "$out" | tail -15 >&2
    overall=1
  elif ! grep -qE "$expect" "$out"; then
    echo "FAIL: gate '$gate' exited $ec on fixture '$name' but WITHOUT the expected cause (/$expect/) — it died for another reason:" >&2
    sed 's/^/    /' "$out" | tail -15 >&2
    overall=1
  else
    echo "OK: bites (exit $ec, expected cause present)"
    bitten=$((bitten + 1))
  fi
  echo
}

# ---- fixtures --------------------------------------------------------------

# 1. check_no_workspace.sh: a go.work in the tree must kill the build.
doctor_workspace() {
  printf 'go 1.26.5\n\nuse .\n' > "$1/go.work"
}

# 2. check_suite_manifest.sh: an unclassified denominator item must kill it.
#    Doctoring: drop the quark catch-all rule, orphaning every quark item the
#    specific rules do not classify.
doctor_unclassified() {
  awk '/^  - match: "quark:\*"$/ { skip = 3 } skip > 0 { skip--; next } { print }' \
    "$1/suite-manifest.yaml" > "$1/suite-manifest.yaml.tmp" \
    && mv "$1/suite-manifest.yaml.tmp" "$1/suite-manifest.yaml"
  ! grep -q '"quark:\*"' "$1/suite-manifest.yaml"
}

# 3. check_suite_manifest.sh: manifest pins drifting from go.mod must kill it.
doctor_pin_drift() {
  sed -E 's/^(  quark:) v[0-9][0-9.]*/\1 v0.0.1-doctored/' \
    "$1/suite-manifest.yaml" > "$1/suite-manifest.yaml.tmp" \
    && mv "$1/suite-manifest.yaml.tmp" "$1/suite-manifest.yaml"
  grep -q 'v0.0.1-doctored' "$1/suite-manifest.yaml"
}

# 4. check_human_labels.sh: a fossil module version in README must kill it.
doctor_readme_label() {
  sed -E 's/quark v[0-9]+\.[0-9]+\.[0-9]+/quark v0.0.1/' \
    "$1/README.md" > "$1/README.md.tmp" && mv "$1/README.md.tmp" "$1/README.md"
  grep -q 'quark v0.0.1' "$1/README.md"
}

# 5. check_human_labels.sh: a fossil `go get …@vX` in the TUTORIAL must kill it.
doctor_tutorial_label() {
  sed -E 's#(github\.com/jcsvwinston/quark)@v[0-9]+\.[0-9]+\.[0-9]+#\1@v0.0.1#' \
    "$1/docs/TUTORIAL.md" > "$1/docs/TUTORIAL.md.tmp" \
    && mv "$1/docs/TUTORIAL.md.tmp" "$1/docs/TUTORIAL.md"
  grep -q 'quark@v0.0.1' "$1/docs/TUTORIAL.md"
}

# 6. check_human_labels.sh: a manifest suite: label that does not match the
#    go.mod set comment must kill it.
doctor_suite_number() {
  sed -E 's/^suite: "[0-9.]+"/suite: "0.0.0"/' \
    "$1/suite-manifest.yaml" > "$1/suite-manifest.yaml.tmp" \
    && mv "$1/suite-manifest.yaml.tmp" "$1/suite-manifest.yaml"
  grep -q '^suite: "0.0.0"' "$1/suite-manifest.yaml"
}

run_fixture workspace_file        "./scripts/check_no_workspace.sh"   "workspace file"       doctor_workspace
run_fixture manifest_unclassified "./scripts/check_suite_manifest.sh" "unclassified"         doctor_unclassified
run_fixture manifest_pin_drift    "./scripts/check_suite_manifest.sh" "pin drift"            doctor_pin_drift
run_fixture readme_fossil_label   "./scripts/check_human_labels.sh"   "README.*fossil label" doctor_readme_label
run_fixture tutorial_fossil_label "./scripts/check_human_labels.sh"   "TUTORIAL.*fossil label" doctor_tutorial_label
run_fixture manifest_suite_label  "./scripts/check_human_labels.sh"   "suite-manifest.yaml suite:" doctor_suite_number

# ---- anti-fossil: every gate script must be covered by a fixture above -----
covered="check_no_workspace.sh check_suite_manifest.sh check_human_labels.sh"
for g in scripts/check_*.sh; do
  base="$(basename "$g")"
  case " $covered " in
    *" $base "*) ;;
    *)
      echo "FAIL: gate $base has no fixture in guard_fixtures.sh — every gate needs its negative executed in CI" >&2
      overall=1
      ;;
  esac
done

echo "== fixtures executed: $total · gates that bite: $bitten =="
if [ "$overall" -ne 0 ]; then
  echo "guard_fixtures: FAIL — dead gates, wrong causes, broken fixtures or uncovered gates (see above)" >&2
  exit 1
fi
echo "guard_fixtures: OK — every gate bites on its doctored tree with the expected cause"
