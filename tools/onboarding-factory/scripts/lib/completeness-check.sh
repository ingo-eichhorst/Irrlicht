#!/usr/bin/env bash
# completeness-check.sh — did this run actually FINISH, or was it torn down
# mid-flight?
#
# Usage:
#   scripts/lib/completeness-check.sh <staging-dir> [--strict]
#
# Outputs JSON to stdout:
#   {"verdict": "complete"|"suspect"|"unknown", "reasons": [...], "sessions": {...}}
#
# Exit 0 always, unless --strict is passed and the verdict is "suspect" (exit 1).
# run-cell.sh calls it WITHOUT --strict: the verdict is recorded in
# run-manifest.json and printed, but it does not fail the run. See "Why advisory"
# below.
#
# Why this exists (#1333, finding A3). `driver.exit-reason` is not evidence that
# a recording is complete. Of three broken copilot runs, TWO reported `ok` with a
# silently truncated recording — which is worse than a timeout, because `ok`
# invites promotion:
#
#   2-6_long-agentic-session-stress  raw assistant.turn_end tally   -> ok
#   3-5_workflow-fanout              subagent prompts counted as parent turns -> ok
#   1-6_checkpoint-rewind            rewind shrinks the transcript  -> timeout
#
# 2-6 was torn down 4s into its final turn, before the daemon's 2s debounce
# flushed the settle: its events.jsonl ends on debounce_coalesced with no final
# transition at all. 3-5 was killed 14s into three 25s children. Both were caught
# only because a human read the events by hand before promoting. This is that
# read, mechanized, and it runs on EVERY outcome — including ok.
#
# Why advisory rather than fatal. Measured against all 370 committed recordings,
# a hard gate on these assertions would fail ~7% of them, and the failures are
# not noise: 1-2_session-end, 2-20_esc-interrupt, 2-9_token-quota-exhausted and
# 3-4_subagent-orphan-cleanup END UNSETTLED ON PURPOSE. Making it fatal would
# either break re-recording for ~20 legitimate cells or require back-filling a
# waiver into each of their committed recipes. So the default turns an invisible
# failure into a loud, recorded one at the moment it matters (before promote),
# and a cell that ends unsettled by design can declare it:
#
#   <staging>/completeness-waiver.json   {"ends_unsettled": true}
#
# --strict is there so hardening this into a gate later is one flag, not a
# rewrite.

set -uo pipefail

STAGING="${1:-}"
STRICT=0
[[ "${2:-}" == "--strict" ]] && STRICT=1

emit() {
  local verdict="$1" reasons_json="$2" sessions_json="${3:-}"
  [[ -n "$sessions_json" ]] || sessions_json='{}'
  jq -nc \
    --arg verdict "$verdict" \
    --argjson reasons "$reasons_json" \
    --argjson sessions "$sessions_json" \
    '{verdict: $verdict, reasons: $reasons, sessions: $sessions}'
  if [[ "$STRICT" == "1" && "$verdict" == "suspect" ]]; then exit 1; fi
  exit 0
}

# Accept either shape of staging dir, because both callers are real:
#   - run-cell.sh's staging ROOT, where the curated fixture is nested under
#     replaydata/agents/<agent>/scenarios/<folder>/ (this is what
#     promote-recording.sh is handed, so the pre-promote check reads the same
#     tree promote will copy);
#   - a promoted recordings/<name>/ dir, which has events.jsonl at its top level
#     (so the check can be re-run by hand against committed data).
EVENTS=""
if [[ -n "$STAGING" && -d "$STAGING" ]]; then
  if [[ -s "$STAGING/events.jsonl" ]]; then
    EVENTS="$STAGING/events.jsonl"
  else
    EVENTS="$(find "$STAGING/replaydata" -path '*/scenarios/*/events.jsonl' -type f 2>/dev/null | sort | head -n1)"
  fi
fi
if [[ -z "$EVENTS" || ! -s "$EVENTS" ]]; then
  emit "unknown" '["no events.jsonl in staging dir"]'
fi

# --- Which sessions were DRIVEN? -----------------------------------------
# session.uuids is the driver's staging contract (one id per line, retired
# rotations appended). Presession `proc-<PID>` rows are excluded everywhere:
# they are transient placeholders that reconcile into a real session and never
# settle on their own, so counting them would fire on every presession adapter.
DRIVEN_JSON="[]"
if [[ -s "$STAGING/session.uuids" ]]; then
  DRIVEN_JSON="$(grep -v '^proc-' "$STAGING/session.uuids" 2>/dev/null \
    | jq -Rsc 'split("\n") | map(select(length > 0))')"
fi

# --- Assertions ----------------------------------------------------------
# 1. Sessions left in `working` by their LAST transition — the run ended while
#    the agent was still going. Covers both the parent (2-6) and a child killed
#    mid-flight (3-5), because a subagent is an ordinary session in the daemon's
#    recording; there is no adapter-specific transcript parsing here.
# 2. The recording's last event is activity the daemon never resolved into a
#    transition — the 2-6 shape exactly.
# 3. No state transitions at all: the daemon observed nothing worth recording.
ANALYSIS="$(jq -sc --argjson driven "$DRIVEN_JSON" '
  [ .[] | select(.kind == "state_transition") ]                as $st
  | ( $st
      | group_by(.session_id)
      | map(sort_by(.seq) | {sid: .[0].session_id, last: .[-1].new_state})
      | map(select(.sid | startswith("proc-") | not))
      | map(select(($driven | length) == 0 or (.sid as $s | $driven | index($s)) != null))
    )                                                          as $per
  | {
      transitions:  ($st | length),
      unsettled:    ($per | map(select(.last == "working") | .sid)),
      last_kind:    (.[-1].kind // "")
    }
' "$EVENTS" 2>/dev/null)"

if [[ -z "$ANALYSIS" ]]; then
  emit "unknown" '["events.jsonl is not valid JSONL"]'
fi

TRANSITIONS="$(jq -r '.transitions' <<<"$ANALYSIS")"
LAST_KIND="$(jq -r '.last_kind' <<<"$ANALYSIS")"
UNSETTLED="$(jq -c '.unsettled' <<<"$ANALYSIS")"

# A cell whose scenario ends unsettled by design declares it; the waiver
# suppresses assertion 1 ONLY — a tail the daemon never resolved is a different
# fault and stays reportable either way.
WAIVED=0
if [[ -f "$STAGING/completeness-waiver.json" ]] \
   && [[ "$(jq -r '.ends_unsettled // false' "$STAGING/completeness-waiver.json" 2>/dev/null)" == "true" ]]; then
  WAIVED=1
fi

REASONS=()
if [[ "$TRANSITIONS" == "0" ]]; then
  REASONS+=("no_state_transitions: the daemon recorded no state transition at all")
fi
if [[ "$(jq -r 'length' <<<"$UNSETTLED")" != "0" ]]; then
  if [[ "$WAIVED" == "1" ]]; then
    REASONS+=("waived: ends_unsettled declared for $(jq -r 'join(",")' <<<"$UNSETTLED")")
  else
    while IFS= read -r s; do
      [[ -n "$s" ]] && REASONS+=("unsettled_session: $s was still \"working\" at its last transition")
    done < <(jq -r '.[]' <<<"$UNSETTLED")
  fi
fi
case "$LAST_KIND" in
  transcript_activity|debounce_coalesced)
    REASONS+=("trailing_unresolved_activity: recording ends on $LAST_KIND — the daemon saw activity it never resolved into a transition (likely torn down inside the debounce window)")
    ;;
esac

# "waived" is bookkeeping, not a fault: a run whose ONLY reason is the waiver is
# complete. Anything else present means something really is unresolved.
FAULTS=0
for r in ${REASONS+"${REASONS[@]}"}; do
  [[ "$r" == waived:* ]] || FAULTS=$((FAULTS + 1))
done

REASONS_JSON="$(printf '%s\n' ${REASONS+"${REASONS[@]}"} | jq -Rsc 'split("\n") | map(select(length > 0))')"
SESSIONS_JSON="$(jq -c '{transitions: .transitions, unsettled: .unsettled, last_kind: .last_kind}' <<<"$ANALYSIS")"

if [[ "$FAULTS" -eq 0 ]]; then
  emit "complete" "$REASONS_JSON" "$SESSIONS_JSON"
fi
emit "suspect" "$REASONS_JSON" "$SESSIONS_JSON"
