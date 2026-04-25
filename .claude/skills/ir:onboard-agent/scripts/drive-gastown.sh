#!/usr/bin/env bash
# drive-gastown.sh — run one orchestrator scenario for gastown.
#
# Unlike drive-claudecode.sh (which drives an interactive CLI under a live
# daemon), gastown is a polling orchestrator: its lifecycle is fully
# captured by feeding canned `gt` responses + a seeded GT_ROOT into the
# poller and snapshotting the resulting orchestrator.State. The Go test
# harness in core/adapters/inbound/orchestrators/gastown/replay_test.go
# does that work; this script wraps it so the skill gets a uniform
# `<staging>/run-manifest.json` contract.
#
# Pipeline:
#   1. Stage a writable copy of replaydata/orchestrators/gastown/scenarios/<scenario>/.
#   2. Run `go test ... -update-goldens` against the staged copy
#      (GASTOWN_FIXTURES_DIR points at the staging dir).
#   3. Diff staged goldens vs committed replaydata/ goldens.
#      - identical → verdict OK
#      - differ    → verdict CHANGED (skill summarizer surfaces the diff)
#   4. Emit run-manifest.json.
#
# Usage:
#   drive-gastown.sh <scenario-name>
#
# Outputs under .build/refresh/gastown/<scenario>-<UTC-ts>/:
#   fixtures/<scenario>/...   — staged copy with regenerated goldens
#   test.log                  — go test output
#   run-manifest.json         — verdict + paths

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SKILL_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || true)"
[[ -n "$REPO_ROOT" ]] || { echo "not in a git repo" >&2; exit 1; }

SCENARIOS_JSON="$SKILL_DIR/scenarios.json"

if [[ $# -ne 1 ]]; then
  echo "usage: drive-gastown.sh <scenario-name>" >&2
  exit 2
fi
ADAPTER="gastown"
SCENARIO="$1"

# Look up the orchestrator scenario; refuse if absent.
CELL_JSON="$(jq --arg s "$SCENARIO" '
  .orchestrator_scenarios[]?
  | select(.name == $s)
  | select(.by_orchestrator.gastown)
  | {description, verify, fixture_dir: .by_orchestrator.gastown.fixture_dir}
' "$SCENARIOS_JSON")"
if [[ -z "$CELL_JSON" || "$CELL_JSON" == "null" ]]; then
  echo "orchestrator scenario not found: $SCENARIO" >&2
  exit 1
fi

# `fixture_dir` is repo-relative; resolve against $REPO_ROOT so the JSON is
# the single source of truth. If a maintainer relocates fixtures they only
# update scenarios.json — script keeps working.
FIXTURE_REL="$(jq -r '.fixture_dir // empty' <<<"$CELL_JSON")"
if [[ -z "$FIXTURE_REL" ]]; then
  echo "scenarios.json: orchestrator_scenarios[$SCENARIO].by_orchestrator.gastown.fixture_dir is missing" >&2
  exit 1
fi
COMMITTED_DIR="$REPO_ROOT/$FIXTURE_REL"
[[ -d "$COMMITTED_DIR" ]] || { echo "committed fixture missing: $COMMITTED_DIR" >&2; exit 1; }

# --- Staging --------------------------------------------------------------
TS="$(date -u +%Y%m%dT%H%M%S)"
STAGING="$REPO_ROOT/.build/refresh/$ADAPTER/$SCENARIO-$TS"
# shellcheck source=lib/assert-staging-path.sh
. "$SCRIPT_DIR/lib/assert-staging-path.sh"
mkdir -p "$STAGING/fixtures"
cp -R "$COMMITTED_DIR" "$STAGING/fixtures/$SCENARIO"

# Strip any pre-existing goldens from the staged copy so the test rewrites
# them from scratch — that way "no diff" really means "code regenerated
# byte-identical output."
rm -rf "$STAGING/fixtures/$SCENARIO/golden"

# --- Run the test against the staged copy ---------------------------------
TEST_LOG="$STAGING/test.log"
set +e
GASTOWN_FIXTURES_DIR="$STAGING/fixtures" \
  go test -C "$REPO_ROOT" \
    ./core/adapters/inbound/orchestrators/gastown/ \
    -run "TestGastownReplay/$SCENARIO" \
    -update-goldens -v \
    >"$TEST_LOG" 2>&1
TEST_EXIT=$?
set -e

if [[ $TEST_EXIT -ne 0 ]]; then
  jq -n \
    --arg adapter "$ADAPTER" \
    --arg scenario "$SCENARIO" \
    --arg staging "$STAGING" \
    --arg test_log "$TEST_LOG" \
    --argjson exit_code "$TEST_EXIT" \
    '{adapter: $adapter,
      scenario: $scenario,
      verdict: "ERROR",
      error: "go_test_failed",
      exit_code: $exit_code,
      staging: $staging,
      test_log: $test_log}' \
    > "$STAGING/run-manifest.json"
  echo "ERROR: go test exited $TEST_EXIT — see $TEST_LOG" >&2
  exit 1
fi

# --- Diff staged goldens vs committed -------------------------------------
COMMITTED_GOLDEN="$COMMITTED_DIR/golden"
STAGED_GOLDEN="$STAGING/fixtures/$SCENARIO/golden"

DIFFS=()
while IFS= read -r f; do
  rel="${f#"$STAGED_GOLDEN/"}"
  committed_f="$COMMITTED_GOLDEN/$rel"
  if [[ ! -f "$committed_f" ]] || ! cmp -s "$f" "$committed_f"; then
    DIFFS+=("$rel")
  fi
done < <(find "$STAGED_GOLDEN" -type f -name '*.json' | sort)

# Detect goldens present in committed but missing from staged (a removed tick).
while IFS= read -r f; do
  rel="${f#"$COMMITTED_GOLDEN/"}"
  staged_f="$STAGED_GOLDEN/$rel"
  if [[ ! -f "$staged_f" ]]; then
    DIFFS+=("$rel (removed)")
  fi
done < <(find "$COMMITTED_GOLDEN" -type f -name '*.json' 2>/dev/null | sort)

if [[ ${#DIFFS[@]} -eq 0 ]]; then
  VERDICT="OK"
else
  VERDICT="CHANGED"
fi

# --- Manifest -------------------------------------------------------------
# `${DIFFS[@]}` on an empty array is unbound under `set -u`; build the JSON
# array explicitly only when there's something to encode.
if [[ ${#DIFFS[@]} -eq 0 ]]; then
  DIFFS_JSON="[]"
else
  DIFFS_JSON="$(printf '%s\n' "${DIFFS[@]}" | jq -R . | jq -s .)"
fi

jq -n \
  --arg adapter "$ADAPTER" \
  --arg scenario "$SCENARIO" \
  --arg verdict "$VERDICT" \
  --arg staging "$STAGING" \
  --arg committed_golden_dir "$COMMITTED_GOLDEN" \
  --arg staged_golden_dir "$STAGED_GOLDEN" \
  --arg test_log "$TEST_LOG" \
  --argjson diffs "$DIFFS_JSON" \
  '{adapter: $adapter,
    scenario: $scenario,
    verdict: $verdict,
    staging: $staging,
    committed_golden_dir: $committed_golden_dir,
    staged_golden_dir: $staged_golden_dir,
    test_log: $test_log,
    diffs: $diffs}' \
  > "$STAGING/run-manifest.json"

echo "verdict: $VERDICT"
echo "staging: $STAGING"
if [[ "$VERDICT" == "CHANGED" ]]; then
  echo "differing goldens:"
  printf '  %s\n' "${DIFFS[@]}"
fi
