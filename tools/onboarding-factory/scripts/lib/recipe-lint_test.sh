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

# recipe-lint reads the per-agent cell (replaydata/agents/<adapter>/scenarios/
# <folder>/metadata.json → .details.recipe) via shard-lib. Build fixtures and
# point shard-lib at them with IR_SCENARIOS_FILE + IR_AGENTS_DIR.
export IR_SCENARIOS_FILE="$TMP/replaydata/scenarios.json"
export IR_AGENTS_DIR="$TMP/replaydata/agents"
mkdir -p "$(dirname "$IR_SCENARIOS_FILE")"
printf '{"meta":{},"scenarios":[]}\n' > "$IR_SCENARIOS_FILE"
# shard <name> <recipe-json> — write the agent "fake"'s metadata.json (keyed by
# scenario_id == <name>) carrying the recipe. recipe-lint reads it by scenario_id.
shard() {
  local name="$1" recipe="$2"
  local cell="$IR_AGENTS_DIR/fake/scenarios/$name"
  mkdir -p "$cell"
  printf '{"scenario_id":"%s","details":{"recipe":%s}}\n' "$name" "$recipe" > "$cell/metadata.json"
  return 0
}

# Fixture driver: a sparse interactive driver dispatching on $type, with one
# grouped arm (send|slash) and a default. Mirrors the real drivers' shape,
# including the #508 #4 DRIVE_ELICITS contract: it dispatches `slash` (a case
# arm) but does NOT elicit it, and requires a dedicated slash step — so the
# semantic lint reads these constants straight from the driver, no manifest.
cat > "$TMP/drive-fake-interactive.sh" <<'SH'
#!/usr/bin/env bash
DRIVE_ELICITS="send sleep wait_turn"
DRIVE_SLASH_REQUIRES_STEP_TYPE=true
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
# A driver with NO DRIVE_ELICITS constant exercises the grammar-only fallback.
cat > "$TMP/drive-bare-interactive.sh" <<'SH'
#!/usr/bin/env bash
case "$type" in
  send|slash) step_send "$x" ;;
  *) echo "unknown step type: $type" >&2 ;;
esac
SH

# Fixture catalog: one cell whose recipe stays in grammar, one that needs a
# missing `sigkill`, and one headless prompt cell (no script).
shard ok-cell  '{"script":[{"type":"send","text":"hi"},{"type":"wait_turn"},{"type":"sleep","seconds":4}]}'
shard gap-cell '{"script":[{"type":"send","text":"hi"},{"type":"sigkill"},{"type":"resume"}]}'
shard headless '{"prompt":"reply ok"}'

fails=0
pass() {
  local label="$1"
  echo "  PASS: $label"
  return 0
}
fail() {
  local label="$1" expected="$2" got="$3"
  echo "  FAIL: $label — expected [$expected] got [$got]"
  fails=$((fails + 1))
  return 0
}
assert_eq() {
  local label="$1" expected="$2" actual="$3"
  [[ "$expected" == "$actual" ]] && pass "$label" || fail "$label" "$expected" "$actual"
  return 0
}

echo "== driver_step_types_from_file: case-arm extraction (splits send|slash) =="
assert_eq "handled set is sorted-unique, grouped arm split, default dropped" \
  "$(printf 'send\nslash\nsleep\nwait_turn')" \
  "$(driver_step_types_from_file "$TMP/drive-fake-interactive.sh")"
assert_eq "missing driver file → empty (no crash)" \
  "" "$(driver_step_types_from_file "$TMP/nope.sh")"

echo "== recipe_step_types =="
assert_eq "ok-cell needs send/sleep/wait_turn" \
  "$(printf 'send\nsleep\nwait_turn')" \
  "$(recipe_step_types ok-cell fake)"
assert_eq "headless cell has no step types" \
  "" "$(recipe_step_types headless fake)"

echo "== recipe_lint_gaps =="
recipe_lint_gaps "$TMP/drive-fake-interactive.sh" ok-cell fake >/dev/null
assert_eq "in-grammar cell → rc 0 (no gap)" "0" "$?"
gaps="$(recipe_lint_gaps "$TMP/drive-fake-interactive.sh" gap-cell fake)"
rc=$?
assert_eq "gap cell → rc 1" "1" "$rc"
assert_eq "gap cell → reports the two missing primitives" \
  "$(printf 'resume\nsigkill')" "$gaps"
recipe_lint_gaps "$TMP/drive-fake-interactive.sh" headless fake >/dev/null
assert_eq "headless cell → rc 0 (no script, no gap)" "0" "$?"

echo "== recipe_semantic_gaps: accepts-vs-elicits + slash-in-send, read from driver (#508 #4) =="
# The fake driver declares DRIVE_ELICITS="send sleep wait_turn" (NOT slash, which
# it dispatches but doesn't elicit) and DRIVE_SLASH_REQUIRES_STEP_TYPE=true.
FAKE_DRV="$TMP/drive-fake-interactive.sh"
# A cell that drives a `slash` step (in grammar, NOT elicited) and a send-text
# slash command (the no-op trap).
shard clean      '{"script":[{"type":"send","text":"hi"},{"type":"wait_turn"}]}'
shard slash-step '{"script":[{"type":"slash","text":"/new"},{"type":"wait_turn"}]}'
shard send-slash '{"script":[{"type":"send","text":"/undo"},{"type":"wait_turn"}]}'
recipe_semantic_gaps "$FAKE_DRV" clean fake >/dev/null
assert_eq "clean cell → rc 0 (every step elicited)" "0" "$?"
out="$(recipe_semantic_gaps "$FAKE_DRV" slash-step fake)"; rc=$?
assert_eq "slash step not in elicits → rc 1" "1" "$rc"
assert_eq "slash step → not-elicited:slash" "not-elicited:slash" "$out"
out="$(recipe_semantic_gaps "$FAKE_DRV" send-slash fake)"; rc=$?
assert_eq "send-text slash on slash_requires adapter → rc 1" "1" "$rc"
assert_eq "send-slash → slash-in-send:/undo" "slash-in-send:/undo" "$out"
assert_eq "driver with no DRIVE_ELICITS → rc 0 (grammar-only)" "0" \
  "$(recipe_semantic_gaps "$TMP/drive-bare-interactive.sh" slash-step fake >/dev/null; echo $?)"
assert_eq "missing driver file → rc 0 (grammar-only)" "0" \
  "$(recipe_semantic_gaps "$TMP/no-such-driver.sh" slash-step fake >/dev/null; echo $?)"

echo "== DRIVE_ELICITS extraction tolerates trailing comments + single quotes (no silent fail-open) =="
# A trailing comment / single quotes must NOT degrade the semantic check to
# grammar-only (the manifest-drift class #508 #4 closed; the sed extractor must
# parse these common forms, not return empty).
cat > "$TMP/drive-cmt-interactive.sh" <<'SH'
#!/usr/bin/env bash
DRIVE_ELICITS="send sleep wait_turn"   # live-TUI set
DRIVE_SLASH_REQUIRES_STEP_TYPE=true    # headless run stores /cmd as text
case "$type" in send|slash) :;; *) :;; esac
SH
assert_eq "commented DRIVE_ELICITS still parsed" \
  "$(printf 'send\nsleep\nwait_turn')" "$(driver_elicits_from_file "$TMP/drive-cmt-interactive.sh")"
assert_eq "commented DRIVE_SLASH_REQUIRES_STEP_TYPE still true" \
  "true" "$(driver_slash_requires_step_type "$TMP/drive-cmt-interactive.sh")"
out="$(recipe_semantic_gaps "$TMP/drive-cmt-interactive.sh" slash-step fake)"; rc=$?
assert_eq "commented driver still catches not-elicited slash → rc 1" "1" "$rc"
assert_eq "commented driver → not-elicited:slash" "not-elicited:slash" "$out"
cat > "$TMP/drive-sq-interactive.sh" <<'SH'
#!/usr/bin/env bash
DRIVE_ELICITS='send sleep wait_turn'
case "$type" in send) :;; *) :;; esac
SH
assert_eq "single-quoted DRIVE_ELICITS parsed" \
  "$(printf 'send\nsleep\nwait_turn')" "$(driver_elicits_from_file "$TMP/drive-sq-interactive.sh")"

echo "== CLI exit codes =="
bash "$DIR/recipe-lint.sh" ok-cell fake "$TMP/drive-fake-interactive.sh" >/dev/null 2>&1
assert_eq "CLI ok-cell → exit 0 (in grammar + elicited)" "0" "$?"
bash "$DIR/recipe-lint.sh" gap-cell fake "$TMP/drive-fake-interactive.sh" >/dev/null 2>&1
assert_eq "CLI gap-cell → exit 3 (driver_gap)" "3" "$?"
bash "$DIR/recipe-lint.sh" slash-step fake "$TMP/drive-fake-interactive.sh" >/dev/null 2>&1
assert_eq "CLI semantic gap → exit 4" "4" "$?"
bash "$DIR/recipe-lint.sh" headless fake "$TMP/drive-fake-interactive.sh" >/dev/null 2>&1
assert_eq "CLI no-recipe-step (headless prompt) → exit 0" "0" "$?"
bash "$DIR/recipe-lint.sh" no-such-cell fake "$TMP/drive-fake-interactive.sh" >/dev/null 2>&1
assert_eq "CLI absent cell (no shard recipe) → exit 0 with note" "0" "$?"

echo ""
if [[ "$fails" -eq 0 ]]; then
  echo "recipe-lint_test: ALL PASS"
else
  echo "recipe-lint_test: $fails FAILED" >&2
  exit 1
fi
