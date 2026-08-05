#!/usr/bin/env bash
# adapter-tables_test.sh — the per-adapter tables that must agree, and the
# per-adapter files that must exist, checked mechanically. Plain bash (no
# framework). Run directly or via scripts/smoke-test.sh.
#
# Why this exists (#1333). Two separate findings in that issue are the same
# hazard wearing different clothes: a fact about an adapter written down in more
# than one place, where forgetting one copy fails SILENTLY.
#
#   B3  promote-recording.sh kept its own CLI-version table, six entries long
#       while precheck.sh's was ten. copilot / gemini-cli / antigravity /
#       mistral-vibe fell through to the `*)` arm, so every one of their
#       recordings was stamped agent_cli_version "unknown" — the field a release
#       sweep keys on — even with the CLI right there on PATH.
#
#   B4  each driver's turn_count() now lives in a sibling turn-count.sh so it can
#       be tested. A new adapter that defines turn_count inline is invisible to
#       replaydata/_lib/drive/turn-count_test.sh: nothing fails, it is simply not
#       covered.
#
# This was not hypothetical. While the #1333 branch was in flight, #1327 landed
# hermes — adding it to precheck's table and not to promote's, silently
# reopening B3 through a conflict-free rebase. That is precisely the failure a
# clean auto-merge cannot show you, so it gets a test rather than a comment.

set -uo pipefail   # NOT -e: assertions capture non-zero return codes

DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$DIR/../../../.." && pwd)"
PRECHECK="$ROOT/tools/onboarding-factory/scripts/precheck.sh"
PROMOTE="$ROOT/tools/promote-recording.sh"

fails=0
pass() { local label="$1"; echo "  PASS: $label"; return 0; }
fail() { local label="$1" detail="$2"; echo "  FAIL: $label — $detail"; fails=$((fails + 1)); return 0; }

# Adapter ids from a `case "$X" in ... esac` block whose arms assign CLI_BIN.
# Skips the `*)` catch-all.
adapters_from() {
  local file="$1"
  grep -oE '^[[:space:]]*[a-z][a-z0-9-]*\)[[:space:]]*CLI_BIN=' "$file" \
    | sed -E 's/^[[:space:]]*//; s/\).*//' | sort -u
}

echo "== precheck.sh and promote-recording.sh know the same adapters (B3) =="
pre="$(adapters_from "$PRECHECK")"
pro="$(adapters_from "$PROMOTE")"
if [[ "$pre" == "$pro" ]]; then
  pass "both tables list: $(echo "$pre" | tr '\n' ' ')"
else
  only_pre="$(comm -23 <(echo "$pre") <(echo "$pro") | tr '\n' ' ')"
  only_pro="$(comm -13 <(echo "$pre") <(echo "$pro") | tr '\n' ' ')"
  fail "the CLI-version tables diverged" \
       "only in precheck.sh: [${only_pre:-none}]  only in promote-recording.sh: [${only_pro:-none}] — an adapter missing from promote is stamped agent_cli_version \"unknown\""
fi

echo "== every driver that counts turns has an extracted, covered counter (B4) =="
TEST_FILE="$ROOT/replaydata/_lib/drive/turn-count_test.sh"
for d in "$ROOT"/replaydata/agents/*/driver-interactive.sh; do
  [[ -f "$d" ]] || continue
  agent="$(basename "$(dirname "$d")")"
  lib="$ROOT/replaydata/agents/$agent/turn-count.sh"

  if grep -qE '^turn_count\(\)' "$d"; then
    fail "$agent: turn_count is defined inline in driver-interactive.sh" \
         "extract it to turn-count.sh — a driver executes its recipe at source time, so an inline counter cannot be tested"
    continue
  fi

  # No counter at all is legitimate: opencode is headless and hermes is a
  # store adapter, so neither has turns to count in a transcript.
  if [[ ! -f "$lib" ]]; then
    if grep -q 'turn-count.sh' "$d"; then
      fail "$agent: driver sources turn-count.sh but the file is missing" "expected $lib"
    else
      pass "$agent: no turn counter (nothing to cover)"
    fi
    continue
  fi

  if ! grep -qE '^turn_count\(\)' "$lib"; then
    fail "$agent: turn-count.sh does not define turn_count" "expected a turn_count() in $lib"
    continue
  fi
  if ! grep -q "turn-count.sh" "$d"; then
    fail "$agent: turn-count.sh exists but the driver never sources it" "the driver would call an undefined turn_count"
    continue
  fi
  if ! grep -q "\"$agent\"\|[[:space:]]$agent[[:space:]]" "$TEST_FILE"; then
    fail "$agent: extracted counter is not exercised by turn-count_test.sh" \
         "add it to the adapter list so a regression in its counter is caught"
    continue
  fi
  pass "$agent: extracted, sourced, and covered"
done

echo ""
if [[ "$fails" -eq 0 ]]; then
  echo "adapter-tables_test: ALL PASS"
else
  echo "adapter-tables_test: $fails FAILED" >&2
  exit 1
fi
