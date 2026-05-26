#!/usr/bin/env bash
# catalog-drift_test.sh — unit tests for lib/catalog-drift.sh. Plain bash + jq
# (no framework). Run directly or via scripts/smoke-test.sh. Exits non-zero on
# any failed assertion.
#
# Covers the #496 RC5 bijection gate: a scenarios[] or rollup row that names a
# phantom catalog cell, and a catalog cell missing from the rollup, are hard
# errors; a catalog cell with no recipe is a warning with a paste-ready stub.

set -uo pipefail   # NOT -e: assertions capture non-zero return codes

DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=catalog-drift.sh
source "$DIR/catalog-drift.sh"

command -v jq >/dev/null || { echo "catalog-drift_test: jq is required" >&2; exit 2; }

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

fails=0
pass() { echo "  PASS: $1"; }
fail() { echo "  FAIL: $1 — expected [$2] got [$3]"; fails=$((fails + 1)); }
assert_eq() { [[ "$2" == "$3" ]] && pass "$1" || fail "$1" "$2" "$3"; }

# Clean fixture: catalog {a,b,c}; scenarios recipe a,b (c awaits a recipe);
# rollup {a,b,c}. No phantom rows; c is a recipe-less WARN.
cat > "$TMP/clean.json" <<'JSON'
{"catalog":[{"id":"a"},{"id":"b"},{"id":"c"}],
 "scenarios":[{"name":"a","coverage_id":"a"},{"name":"b","coverage_id":"b"}]}
JSON
cat > "$TMP/clean-rollup.json" <<'JSON'
{"scenarios":[{"id":"a"},{"id":"b"},{"id":"c"}]}
JSON

echo "== cd_errors: clean fixture =="
cd_errors "$TMP/clean.json" "$TMP/clean-rollup.json" >/dev/null
assert_eq "no phantom rows + rollup covers catalog → rc 0" 0 "$?"
assert_eq "recipe-less catalog cell surfaced" c "$(cd_recipeless_catalog_ids "$TMP/clean.json")"

echo "== cd_errors: planted drift =="
# scenarios coverage_id 'z' has no catalog row; rollup id 'y' has no catalog row;
# catalog 'c' is missing from the rollup.
cat > "$TMP/drift.json" <<'JSON'
{"catalog":[{"id":"a"},{"id":"b"},{"id":"c"}],
 "scenarios":[{"name":"a","coverage_id":"a"},{"name":"z","coverage_id":"z"}]}
JSON
cat > "$TMP/drift-rollup.json" <<'JSON'
{"scenarios":[{"id":"a"},{"id":"b"},{"id":"y"}]}
JSON
errs="$(cd_errors "$TMP/drift.json" "$TMP/drift-rollup.json")"; rc=$?
assert_eq "drift → rc 1" 1 "$rc"
grep -q "coverage_id 'z' has no catalog" <<< "$errs" && pass "flags phantom scenarios coverage_id" || fail "phantom scenarios" "z error" "$errs"
grep -q "rollup id 'y' has no catalog"   <<< "$errs" && pass "flags phantom rollup id"          || fail "phantom rollup" "y error" "$errs"
grep -q "catalog\[\] row 'c' is missing from the rollup" <<< "$errs" && pass "flags catalog cell missing from rollup" || fail "catalog-not-in-rollup" "c error" "$errs"

echo "== cd_stub: paste-ready scenarios[] row =="
stub="$(cd_stub "$TMP/clean.json" c)"
assert_eq "stub name == id"         c "$(jq -r '.name' <<< "$stub")"
assert_eq "stub coverage_id == id"  c "$(jq -r '.coverage_id' <<< "$stub")"
assert_eq "stub requires is []"     "[]" "$(jq -c '.requires' <<< "$stub")"

echo "== CLI exit codes =="
bash "$DIR/catalog-drift.sh" "$TMP/clean.json" "$TMP/clean-rollup.json" /nonexistent-specs >/dev/null 2>&1
assert_eq "clean → exit 0" 0 "$?"
bash "$DIR/catalog-drift.sh" "$TMP/drift.json" "$TMP/drift-rollup.json" /nonexistent-specs >/dev/null 2>&1
assert_eq "drift → exit 1" 1 "$?"

echo "== .specs source leg (best-effort, only when present) =="
printf '# catalog\n\n- `a` — feature\n- `phantom-source-cell` — not in catalog\n' > "$TMP/specs.md"
out="$(bash "$DIR/catalog-drift.sh" "$TMP/clean.json" "$TMP/clean-rollup.json" "$TMP/specs.md" 2>&1)"
assert_eq ".specs present + clean catalogs → still exit 0" 0 "$?"
grep -q "phantom-source-cell" <<< "$out" && pass ".specs leg warns on a source id with no catalog row" || fail ".specs leg" "phantom-source-cell warned" "$out"

echo ""
if [[ "$fails" -eq 0 ]]; then
  echo "catalog-drift_test: ALL PASS"
else
  echo "catalog-drift_test: $fails FAILED" >&2
  exit 1
fi
