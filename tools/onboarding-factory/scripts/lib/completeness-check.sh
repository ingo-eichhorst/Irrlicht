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

# --- Sourceable wrapper --------------------------------------------------
# Both rigs need the same three things (invoke, fall back, warn), and #1214 is
# the standing lesson about what happens when run-cell.sh and run-cell-multi.sh
# keep their own copies of a shared step: a fix reaches only whichever one the
# author was editing. Sourcing this file defines the helper without running the
# CLI below, the same dual shape lib/cell-integrity.sh uses.
#
#   completeness_json <staging> [--scenario <folder-or-name>]
#     Echoes the verdict JSON, ALWAYS valid and non-empty. Covers both failure
#     shapes: a non-zero exit, and a zero exit with empty stdout (which the
#     `||` fallback alone would miss, breaking the caller's --argjson).
completeness_json() {
  local out
  out="$(bash "${BASH_SOURCE[0]}" "$@" 2>/dev/null)" \
    || out='{"verdict":"unknown","reasons":["completeness-check failed to run"],"sessions":{}}'
  [[ -n "$out" ]] \
    || out='{"verdict":"unknown","reasons":["completeness-check produced no output"],"sessions":{}}'
  echo "$out"
}

#   report_completeness <verdict-json>  → prints the reasons on a non-complete
#     verdict; returns 0 either way (advisory, see the header).
report_completeness() {
  local json="$1" verdict
  verdict="$(jq -r '.verdict' <<<"$json" 2>/dev/null || echo unknown)"
  if [[ "$verdict" != "complete" ]]; then
    echo "completeness: $verdict — DO NOT PROMOTE without reading these:" >&2
    jq -r '.reasons[]? | "  - " + .' <<<"$json" >&2 || true
  fi
}

# Sourced rather than executed → helpers are defined, nothing else runs.
(return 0 2>/dev/null) && return 0

STAGING="${1:-}"
STRICT=0
SCENARIO=""
shift || true
while [[ $# -gt 0 ]]; do
  case "$1" in
    --strict)   STRICT=1 ;;
    --scenario) shift; SCENARIO="${1:-}" ;;
    *) ;;
  esac
  shift || true
done

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
#
# CROSS-ADAPTER CAVEAT: run-cell-multi.sh stages one curated events.jsonl per
# adapter under this tree, and the `head -n1` below reads only the
# alphabetically-first. A multi-adapter cell therefore gets a verdict describing
# one adapter's view. It is still a real check on real data — just narrower than
# the single-adapter path, and it never reports a false `complete` for the
# adapter it did read. (The multi rig also writes session.uuids per adapter
# subdir rather than at the staging root, so DRIVEN_JSON is empty there and the
# filter falls back to "all non-proc sessions" — same direction: weaker, not
# wrong.) Both are worth tightening when a multi-adapter cell next needs it.
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
#
# The exclusion is CONDITIONAL, and it has to be. A `proc-<pid>` row is a
# placeholder only when it reconciles into something else — and for aider it
# never does: `proc-<pid>` IS its terminal session id. An unconditional
# `startswith("proc-") | not` therefore made the whole unsettled-session
# assertion a structural no-op for one entire adapter, handing every aider cell
# a `complete` verdict that proved nothing about settledness.
#
# The discriminator needs no adapter identity and no daemon change: a presession
# is a placeholder exactly when the recording ALSO contains a non-`proc-`
# session. Swept over all 370 committed recordings, 29 have zero non-proc
# state transitions and all 29 are aider — no false positives, and the rule
# covers the next presession-terminal adapter automatically instead of leaving a
# second silent hole.
# Same conditional rule for the driver's own contract: drop `proc-` rows only
# when a real session id is also present, so an aider run isn't filtered to an
# empty driven set.
DRIVEN_JSON="[]"
if [[ -s "$STAGING/session.uuids" ]]; then
  DRIVEN_JSON="$(jq -Rsc '
    split("\n") | map(select(length > 0))
    | . as $all
    | ($all | map(select(startswith("proc-") | not))) as $real
    | if ($real | length) > 0 then $real else $all end' < "$STAGING/session.uuids")"
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
  # A presession is a placeholder only if a real session also appears here.
  | ( $st | map(select(.session_id | startswith("proc-") | not)) | length > 0 ) as $has_real
  | ( $st
      | group_by(.session_id)
      | map(sort_by(.seq) | {sid: .[0].session_id, last: .[-1].new_state})
      | map(select(if $has_real then (.sid | startswith("proc-") | not) else true end))
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

# A scenario that ends unsettled BY DESIGN declares it in the committed catalog:
#
#   replaydata/agents/scenarios.json  →  .meta.ends_unsettled: ["session-end", …]
#
# That is where it belongs, not in a per-run staging file: "this scenario's
# definition requires an unsettled ending" is a durable, agent-agnostic property
# of the scenario, and a staging-only waiver was unreachable in practice —
# run-cell.sh creates the staging dir, drives the agent and runs this check in
# one unbroken sequence, so nobody ever had a window to drop the file in.
#
# The list is deliberately NARROW. Membership means the scenario is *defined* to
# end unsettled (the process is killed, the turn is interrupted, the quota dies,
# the orphan is abandoned) — NOT merely that recordings of it have ended
# unsettled before. Twelve scenarios currently produce a `suspect` verdict
# somewhere in the corpus; waiving all of them would hollow the check out into
# exactly the green-and-vacuous pass this whole issue is about.
#
# $STAGING/completeness-waiver.json still works as a per-run override.
# Either way the waiver suppresses assertion 1 ONLY — a tail the daemon never
# resolved is a different fault and stays reportable.
WAIVED=0
if [[ -n "$SCENARIO" ]]; then
  # Accept a folder ("1-2_session-end") or a bare catalog name ("session-end").
  SCEN_NAME="$(sed -E 's/^[0-9]+-[0-9]+_//' <<<"$SCENARIO")"
  CATALOG="$(git -C "$(dirname "$STAGING")" rev-parse --show-toplevel 2>/dev/null || git rev-parse --show-toplevel 2>/dev/null || true)/replaydata/agents/scenarios.json"
  if [[ -f "$CATALOG" ]] \
     && [[ "$(jq -r --arg s "$SCEN_NAME" '(.meta.ends_unsettled // []) | index($s) != null' "$CATALOG" 2>/dev/null)" == "true" ]]; then
    WAIVED=1
  fi
fi
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
# $ANALYSIS already IS {transitions, unsettled, last_kind} — re-projecting those
# same three keys would just be a second copy of the key list to keep in sync.
SESSIONS_JSON="$ANALYSIS"

if [[ "$FAULTS" -eq 0 ]]; then
  emit "complete" "$REASONS_JSON" "$SESSIONS_JSON"
fi
emit "suspect" "$REASONS_JSON" "$SESSIONS_JSON"
