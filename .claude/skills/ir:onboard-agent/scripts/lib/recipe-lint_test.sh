#!/usr/bin/env bash
# recipe-lint_test.sh — unit tests for lib/recipe-lint.sh. Plain bash + jq
# (no framework). Run directly or via scripts/smoke-test.sh. Exits non-zero
# on any failed assertion.
#
# Covers the #476 record-time backstop: a recipe step type the driver
# doesn't implement must be caught by static inspection (gap:<primitive>)
# before a recording is ever attempted.

set -uo pipefail   # NOT -e: assertions capture non-zero return codes

DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=recipe-lint.sh
source "$DIR/recipe-lint.sh"

command -v jq >/dev/null || { echo "recipe-lint_test: jq is required" >&2; exit 2; }

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# Fixture driver: a sparse interactive driver dispatching on $type, with one
# grouped arm (send|slash) and a default. Mirrors the real drivers' shape.
cat > "$TMP/drive-fake-interactive.sh" <<'SH'
#!/usr/bin/env bash
case "$type" in
  send|slash)
    step_send "$x" ;;
  wait_turn)
    step_wait_turn ;;
  sleep)
    sleep "$n" ;;
  *)
    echo "unknown step type: $type" >&2 ;;
esac
SH

# Fixture catalog: one cell whose recipe stays in grammar, one that needs a
# missing `sigkill`, and one headless prompt cell (no script).
cat > "$TMP/scenarios.json" <<'JSON'
{"scenarios":[
  {"name":"ok-cell","by_adapter":{"fake":{"script":[
    {"type":"send","text":"hi"},{"type":"wait_turn"},{"type":"sleep","seconds":4}]}}},
  {"name":"gap-cell","by_adapter":{"fake":{"script":[
    {"type":"send","text":"hi"},{"type":"sigkill"},{"type":"resume"}]}}},
  {"name":"headless","by_adapter":{"fake":{"prompt":"reply ok"}}}
]}
JSON

fails=0
pass() { echo "  PASS: $1"; }
fail() { echo "  FAIL: $1 — expected [$2] got [$3]"; fails=$((fails + 1)); }
assert_eq() { [[ "$2" == "$3" ]] && pass "$1" || fail "$1" "$2" "$3"; }

echo "== driver_step_types_from_file: case-arm extraction (splits send|slash) =="
assert_eq "handled set is sorted-unique, grouped arm split, default dropped" \
  "$(printf 'send\nslash\nsleep\nwait_turn')" \
  "$(driver_step_types_from_file "$TMP/drive-fake-interactive.sh")"
assert_eq "missing driver file → empty (no crash)" \
  "" "$(driver_step_types_from_file "$TMP/nope.sh")"

echo "== recipe_step_types_from_json =="
assert_eq "ok-cell needs send/sleep/wait_turn" \
  "$(printf 'send\nsleep\nwait_turn')" \
  "$(recipe_step_types_from_json "$TMP/scenarios.json" ok-cell fake)"
assert_eq "headless cell has no step types" \
  "" "$(recipe_step_types_from_json "$TMP/scenarios.json" headless fake)"

echo "== recipe_lint_gaps =="
recipe_lint_gaps "$TMP/drive-fake-interactive.sh" "$TMP/scenarios.json" ok-cell fake >/dev/null
assert_eq "in-grammar cell → rc 0 (no gap)" "0" "$?"
gaps="$(recipe_lint_gaps "$TMP/drive-fake-interactive.sh" "$TMP/scenarios.json" gap-cell fake)"
rc=$?
assert_eq "gap cell → rc 1" "1" "$rc"
assert_eq "gap cell → reports the two missing primitives" \
  "$(printf 'resume\nsigkill')" "$gaps"
recipe_lint_gaps "$TMP/drive-fake-interactive.sh" "$TMP/scenarios.json" headless fake >/dev/null
assert_eq "headless cell → rc 0 (no script, no gap)" "0" "$?"

echo "== recipe_semantic_gaps: accepts-vs-elicits + slash-in-send (#496 RC3) =="
# Manifest fixture: fake elicits send/sleep/wait_turn but NOT slash (the driver
# dispatches slash but doesn't elicit it), and requires a dedicated slash step.
cat > "$TMP/manifest.json" <<'JSON'
{"adapters":{"fake":{"elicits":["send","sleep","wait_turn"],"slash_requires_step_type":true}}}
JSON
# A cell that drives a `slash` step (in grammar, NOT elicited) and a send-text
# slash command (the no-op trap).
cat > "$TMP/sem.json" <<'JSON'
{"scenarios":[
  {"name":"clean","by_adapter":{"fake":{"script":[{"type":"send","text":"hi"},{"type":"wait_turn"}]}}},
  {"name":"slash-step","by_adapter":{"fake":{"script":[{"type":"slash","text":"/new"},{"type":"wait_turn"}]}}},
  {"name":"send-slash","by_adapter":{"fake":{"script":[{"type":"send","text":"/undo"},{"type":"wait_turn"}]}}}
]}
JSON
recipe_semantic_gaps "$TMP/manifest.json" "$TMP/sem.json" clean fake >/dev/null
assert_eq "clean cell → rc 0 (every step elicited)" "0" "$?"
out="$(recipe_semantic_gaps "$TMP/manifest.json" "$TMP/sem.json" slash-step fake)"; rc=$?
assert_eq "slash step not in elicits → rc 1" "1" "$rc"
assert_eq "slash step → not-elicited:slash" "not-elicited:slash" "$out"
out="$(recipe_semantic_gaps "$TMP/manifest.json" "$TMP/sem.json" send-slash fake)"; rc=$?
assert_eq "send-text slash on slash_requires adapter → rc 1" "1" "$rc"
assert_eq "send-slash → slash-in-send:/undo" "slash-in-send:/undo" "$out"
recipe_semantic_gaps "$TMP/scenarios.json" "$TMP/sem.json" clean fake >/dev/null   # absent manifest entry path
assert_eq "adapter absent from manifest → rc 0 (grammar-only)" "0" \
  "$(recipe_semantic_gaps "$TMP/no-such-manifest.json" "$TMP/sem.json" slash-step fake >/dev/null; echo $?)"

echo "== CLI exit codes =="
bash "$DIR/recipe-lint.sh" "$TMP/scenarios.json" ok-cell fake "$TMP/drive-fake-interactive.sh" >/dev/null 2>&1
assert_eq "CLI ok-cell → exit 0 (fake absent from real manifest → grammar-only)" "0" "$?"
bash "$DIR/recipe-lint.sh" "$TMP/scenarios.json" gap-cell fake "$TMP/drive-fake-interactive.sh" >/dev/null 2>&1
assert_eq "CLI gap-cell → exit 3 (driver_gap)" "3" "$?"
bash "$DIR/recipe-lint.sh" "$TMP/sem.json" slash-step fake "$TMP/drive-fake-interactive.sh" "$TMP/manifest.json" >/dev/null 2>&1
assert_eq "CLI semantic gap → exit 4" "4" "$?"
bash "$DIR/recipe-lint.sh" "$TMP/scenarios.json" headless fake "$TMP/drive-fake-interactive.sh" "$TMP/manifest.json" >/dev/null 2>&1
assert_eq "CLI no-recipe-step (headless prompt) → exit 0" "0" "$?"
bash "$DIR/recipe-lint.sh" "$TMP/sem.json" no-such-cell fake "$TMP/drive-fake-interactive.sh" "$TMP/manifest.json" >/dev/null 2>&1
assert_eq "CLI absent cell (no by_adapter entry) → exit 0 with note" "0" "$?"

echo ""
if [[ "$fails" -eq 0 ]]; then
  echo "recipe-lint_test: ALL PASS"
else
  echo "recipe-lint_test: $fails FAILED" >&2
  exit 1
fi
