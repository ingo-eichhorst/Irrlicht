#!/usr/bin/env bash
# codescene-trigger.sh — trigger a fresh full CodeScene analysis of the
# project's configured branch (main) and poll until it completes, or a
# bounded timeout elapses. Prints the resulting analysis JSON in the same
# shape as `codescene-report.sh analyses/latest`, so callers can chain
# straight into the existing hotspot-fetch step. All progress/diagnostic
# messages go to stderr, prefixed "codescene-trigger:" — callers scraping
# combined stdout+stderr logs (e.g. `gh run view --log`) should drop lines
# matching that prefix before parsing the remainder as JSON.
#
# CodeScene's docs say to turn off analysis scheduling if this endpoint is
# used as a *continuous* trigger — this script is a one-off manual trigger
# only; do not wrap it in a cron/schedule.
#
# Known limitation: CodeScene's `run-analysis` response doesn't document a
# job/analysis id to correlate against, so "a fresh analysis is ready" is
# detected only by `analyses/latest`'s id changing from the pre-trigger
# baseline. An unrelated scheduled or concurrently-triggered analysis that
# completes first would be (mis)reported as this invocation's result. Low
# risk for this repo's single-maintainer, on-demand usage; revisit if this
# is ever invoked concurrently or on a schedule.
#
# Requires the same env vars as codescene-report.sh:
#   CODESCENE_API_TOKEN    Bearer token from the CodeScene dashboard's API
#                          tokens page (repo secret in CI; never commit it).
#                          Must have write/trigger scope, not just read —
#                          a read-only token gets a 403 from run-analysis.
#   CODESCENE_PROJECT_ID   This repo's CodeScene project id (82148 for
#                          ingo-eichhorst/Irrlicht).
#
# Optional env vars (both wired through as codescene-report.yml inputs):
#   CODESCENE_POLL_INTERVAL_SECS   seconds between polls (default 30)
#   CODESCENE_POLL_TIMEOUT_SECS    overall bound before giving up (default 900)
#
# Usage:
#   tools/codescene-trigger.sh
set -euo pipefail

: "${CODESCENE_API_TOKEN:?CODESCENE_API_TOKEN env var required}"
: "${CODESCENE_PROJECT_ID:?CODESCENE_PROJECT_ID env var required}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
API_BASE="https://api.codescene.io/v2/projects/${CODESCENE_PROJECT_ID}"
POLL_INTERVAL_SECS="${CODESCENE_POLL_INTERVAL_SECS:-30}"
POLL_TIMEOUT_SECS="${CODESCENE_POLL_TIMEOUT_SECS:-900}"

# Reuses codescene-report.sh's GET rather than re-implementing the
# curl+Bearer-auth call here, so the two scripts can't drift apart.
fetch_latest() {
  local body
  if ! body="$("${SCRIPT_DIR}/codescene-report.sh" analyses/latest)"; then
    echo "codescene-trigger: failed to fetch analyses/latest from CodeScene (network error or non-2xx response)." >&2
    exit 1
  fi
  printf '%s' "$body"
}

# CodeScene's `analyses/latest` schema for an in-progress vs. complete
# analysis isn't documented; treat a missing status/state field as complete
# (this is the common case). Anchored match against known-good values only
# — an unrecognized value (including "incomplete"-style near-misses) is
# treated as not-yet-complete rather than guessed at.
is_complete() {
  jq -e '(.status // .state // "complete") | ascii_downcase | test("^(complete|completed|finished|done|ok|success|successful)$")' \
    >/dev/null 2>&1 <<<"$1"
}

# Broad, unanchored on purpose: any hint of failure/cancellation should
# short-circuit the poll loop immediately rather than waiting out the full
# timeout and misreporting a failed run as a stale/slow one.
is_failed() {
  jq -e '(.status // .state // "") | ascii_downcase | test("fail|error|cancel|abort")' \
    >/dev/null 2>&1 <<<"$1"
}

baseline="$(fetch_latest)"
baseline_id="$(jq -r '.id' <<<"$baseline")"

trigger_response="$(mktemp)"
trap 'rm -f "$trigger_response"' EXIT

http_status="$(curl -s -o "$trigger_response" -w '%{http_code}' \
  -X POST -H "Authorization: Bearer ${CODESCENE_API_TOKEN}" \
  -H "Content-Type: application/json" "${API_BASE}/run-analysis")"

if [[ "$http_status" == "403" ]]; then
  echo "codescene-trigger: run-analysis returned 403 Forbidden — CODESCENE_API_TOKEN is likely scoped read-only and needs to be re-scoped to permit triggering analyses." >&2
  exit 1
fi
if [[ ! "$http_status" =~ ^2 ]]; then
  echo "codescene-trigger: run-analysis failed with HTTP ${http_status}:" >&2
  cat "$trigger_response" >&2
  exit 1
fi

echo "codescene-trigger: triggered a fresh analysis (previous latest id=${baseline_id}); polling every ${POLL_INTERVAL_SECS}s, timeout ${POLL_TIMEOUT_SECS}s..." >&2

elapsed=0
while (( elapsed < POLL_TIMEOUT_SECS )); do
  sleep "$POLL_INTERVAL_SECS"
  elapsed=$(( elapsed + POLL_INTERVAL_SECS ))
  latest="$(fetch_latest)"
  latest_id="$(jq -r '.id' <<<"$latest")"
  if [[ "$latest_id" != "$baseline_id" ]]; then
    if is_complete "$latest"; then
      echo "codescene-trigger: new analysis id=${latest_id} completed after ~${elapsed}s" >&2
      jq . <<<"$latest"
      exit 0
    fi
    if is_failed "$latest"; then
      echo "codescene-trigger: new analysis id=${latest_id} reported a failure status after ~${elapsed}s — not waiting out the full timeout:" >&2
      jq . <<<"$latest" >&2
      exit 1
    fi
  fi
done

echo "codescene-trigger: timed out after ${elapsed}s waiting for a new analysis to complete — falling back to the last completed analysis (id=${baseline_id})." >&2
jq '. + {"_irrlicht_stale_fallback": true}' <<<"$baseline"
