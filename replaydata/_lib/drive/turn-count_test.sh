#!/usr/bin/env bash
# turn-count_test.sh — unit tests for every adapter's turn_count(), run over
# COMMITTED transcripts at chosen prefixes. Plain bash (no framework), matching
# the style of drive-lib_test.sh. Run directly or via
# tools/onboarding-factory/scripts/smoke-test.sh.
#
# Why this exists (#1333, finding B4). Turn accounting is the single most
# defect-prone seam in the drivers and had NO coverage. The copilot onboarding
# run alone burned four live recordings on four separate defects in it:
#
#   11cd0133  `grep -c` prints 0 AND exits 1, so `|| echo 0` emitted two lines
#             and every (( )) comparison downstream died
#   b15a70b0  counted raw assistant.turn_end; copilot closes a turn after EVERY
#             tool call, so one prompt with one bash call emits two
#   ca7bfda4  counted subagent prompts as parent turns; copilot subagents write
#             into the PARENT's transcript, tagged with a top-level agentId
#   462ca380  a monotonic EXPECTED_TURNS can't survive a rewind, which SHRINKS
#             the transcript
#
# Each was found by spending real credits on a live recording.
#
# THIS IS A LOCK, NOT A DEFECT TEST. All four defects are already fixed on main,
# so every assertion here passes by construction — that is expected and is not
# evidence of anything. Its value is the next four defects. The assertions are
# chosen to DISCRIMINATE rather than merely record: 2-6 expects 4 where a raw
# turn_end tally would say 5, and 3-5 expects 1 where counting subagent prompts
# would say 4. A regression to either historical bug fails here.
#
# The corpus is the real committed transcripts (resolved by scenario, not by
# recording-dir name, so a re-record doesn't move the path). Those recordings are
# frozen fixtures — deletion-guarded in CI (#268) — so the counts are stable. If
# a deliberate re-record does change one, this test failing is the correct
# outcome: a changed turn count is worth a human look, not a silent update.

set -uo pipefail   # NOT -e: assertions capture non-zero return codes

DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$DIR/../../.." && pwd)"

command -v jq >/dev/null || { echo "turn-count_test: jq is required" >&2; exit 2; }

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

fails=0
pass() { local label="$1"; echo "  PASS: $label"; return 0; }
fail() { local label="$1" expected="$2" got="$3"; echo "  FAIL: $label — expected [$expected] got [$got]"; fails=$((fails + 1)); return 0; }

# count <agent> <transcript> — source the adapter's counter in a SUBSHELL so nine
# same-named functions can't shadow each other, and call it.
count() {
  local agent="$1" transcript="$2"
  ( set +u
    # shellcheck disable=SC1090
    source "$ROOT/replaydata/agents/$agent/turn-count.sh"
    TRANSCRIPT="$transcript"
    turn_count )
}

# find_transcript <agent> <scenario-dir> [ext]
#
# Two things this deliberately gets right:
#   - `tail -n1`, not `head -n1`: recording dirs are date-stamped, so the LAST
#     one is the newest. Reading the oldest would make the header's promise
#     ("a deliberate re-record changing a count should fail here") a lie — a
#     re-record would add a dir this test never looks at, and it would stay
#     green while the counter's real input changed.
#   - `*/$scen/*`, anchored: an unanchored `*/$scen*/*` would let
#     1-1_session-start also match a future 1-10_…, and "1-10_" sorts BEFORE
#     "1-1_" ('0' < '_'), so the wrong cell could win. 2-1 vs 2-10..2-21 already
#     shows this naming pattern exists.
find_transcript() {
  local agent="$1" scen="$2" ext="${3:-jsonl}"
  find "$ROOT/replaydata/agents/$agent" -path "*/$scen/*" -name "transcript.$ext" 2>/dev/null | sort | tail -n1
}

assert_count() {
  local label="$1" agent="$2" transcript="$3" expected="$4" got
  if [[ -z "$transcript" || ! -f "$transcript" ]]; then
    fail "$label" "$expected" "transcript not found"
    return 0
  fi
  got="$(count "$agent" "$transcript")"
  [[ "$got" == "$expected" ]] && pass "$label" || fail "$label" "$expected" "$got"
  return 0
}

echo "== every adapter's counter loads and counts a one-turn session =="
# Baseline: each adapter's simplest committed recording is a single turn. This is
# the 11cd0133 guard — a counter that prints "0\n0" or a non-numeric blob fails
# the exact-match here before it ever reaches a (( )) comparison.
assert_count "antigravity  1-1"  antigravity  "$(find_transcript antigravity 1-1_session-start)"  1
assert_count "claudecode   1-1"  claudecode   "$(find_transcript claudecode 1-1_session-start)"   1
assert_count "codex        1-1"  codex        "$(find_transcript codex 1-1_session-start)"        1
assert_count "copilot      1-1"  copilot      "$(find_transcript copilot 1-1_session-start)"      1
assert_count "gemini-cli   1-1"  gemini-cli   "$(find_transcript gemini-cli 1-1_session-start)"   1
assert_count "kiro-cli     1-1"  kiro-cli     "$(find_transcript kiro-cli 1-1_session-start)"     1
assert_count "mistral-vibe 1-1"  mistral-vibe "$(find_transcript mistral-vibe 1-1_session-start)" 1
assert_count "pi           1-1"  pi           "$(find_transcript pi 1-1_session-start)"           1
# aider's 1-1 recording never reaches an LLM turn — it captures the OpenRouter
# auth prompt — so its own baseline is a cell that did complete one.
assert_count "aider        2-2"  aider        "$(find_transcript aider 2-2_auto-executed-tool-call md)" 1

echo "== a session that never completed a turn counts 0, not 1 =="
# aider 1-1 is the natural specimen: a real, committed, non-empty transcript with
# no completed turn in it. A counter that keyed on "the file exists" would say 1.
assert_count "aider 1-1 (auth prompt only) -> 0" aider "$(find_transcript aider 1-1_session-start md)" 0

echo "== every counter returns 0 for a missing or empty transcript =="
: > "$TMP/empty.jsonl"
for a in aider antigravity claudecode codex copilot gemini-cli kiro-cli mistral-vibe pi; do
  got="$(count "$a" "$TMP/empty.jsonl")"
  [[ "$got" == "0" ]] && pass "$a: empty -> 0" || fail "$a: empty -> 0" "0" "$got"
  got="$(count "$a" "$TMP/does-not-exist.jsonl")"
  [[ "$got" == "0" ]] && pass "$a: missing -> 0" || fail "$a: missing -> 0" "0" "$got"
done

echo "== copilot 2-6: a tool-call continuation is NOT a turn (guards b15a70b0) =="
# copilot closes a turn after EVERY tool call, so this transcript holds 5
# assistant.turn_end records for 4 prompts. A raw turn_end tally reports 5.
T26="$(find_transcript copilot 2-6_long-agentic-session-stress)"
assert_count "4 prompts, 5 turn_end records -> 4" copilot "$T26" 4
if [[ -f "$T26" ]]; then
  raw="$(grep -c '"type":"assistant\.turn_end"' "$T26" || true)"
  [[ "$raw" == "5" ]] && pass "the corpus still has the discriminating shape (5 raw turn_end)" \
                      || fail "the corpus still has the discriminating shape" "5" "$raw"
fi

echo "== copilot 3-5: subagent prompts are NOT parent turns (guards ca7bfda4) =="
# copilot subagents write into the PARENT's transcript tagged with a top-level
# agentId: 4 user.message records, 3 of them children. Counting them says 4.
T35="$(find_transcript copilot 3-5_workflow-fanout)"
assert_count "4 user.message, 3 tagged agentId -> 1" copilot "$T35" 1
if [[ -f "$T35" ]]; then
  sub="$(grep '"agentId":' "$T35" | grep -c '"type":"user\.message"' || true)"
  [[ "$sub" == "3" ]] && pass "the corpus still has the discriminating shape (3 subagent prompts)" \
                      || fail "the corpus still has the discriminating shape" "3" "$sub"
fi

echo "== copilot 1-6: a rewind-truncated transcript still counts (guards 462ca380) =="
# A checkpoint rewind rewrites events.jsonl in place and DELETES the rewound
# turns' records, so the count goes DOWN. The counter must report the turns that
# still exist rather than diverging; EXPECTED_TURNS absorbs the drop in the
# driver's wait_turn (see copilot/driver-interactive.sh).
assert_count "post-rewind transcript -> 2" copilot "$(find_transcript copilot 1-6_checkpoint-rewind)" 2

echo "== copilot: prefixes — an in-flight turn is not yet complete =="
# The `open` term is what makes a truncated transcript honest: at 20 lines three
# prompts have been sent and the third has no turn_end yet, so two are complete.
if [[ -f "$T26" ]]; then
  head -5  "$T26" > "$TMP/p5.jsonl"
  head -20 "$T26" > "$TMP/p20.jsonl"
  head -34 "$T26" > "$TMP/p34.jsonl"
  assert_count "prefix before the first prompt -> 0" copilot "$TMP/p5.jsonl"  0
  assert_count "prefix mid-third-turn -> 2"          copilot "$TMP/p20.jsonl" 2
  assert_count "full transcript -> 4"                copilot "$TMP/p34.jsonl" 4
fi

echo "== counts are numeric single lines (guards the 0\\n0 class outright) =="
for a in aider antigravity claudecode codex copilot gemini-cli kiro-cli mistral-vibe pi; do
  ext=jsonl; [[ "$a" == aider ]] && ext=md
  t="$(find_transcript "$a" 1-1_session-start "$ext")"
  [[ -f "$t" ]] || continue
  lines="$(count "$a" "$t" | wc -l | tr -d ' ')"
  [[ "$lines" == "1" ]] && pass "$a: exactly one line of output" || fail "$a: exactly one line of output" "1" "$lines"
done

echo ""
if [[ "$fails" -eq 0 ]]; then
  echo "turn-count_test: ALL PASS"
else
  echo "turn-count_test: $fails FAILED" >&2
  exit 1
fi
