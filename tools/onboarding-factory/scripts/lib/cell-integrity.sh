#!/usr/bin/env bash
# cell-integrity.sh — artifact-completeness gate (#496 RC6): a RECORDED cell
# must carry its full, consistent artifact set, so "recorded" can't be a
# partial set that replay-fixtures reports as a vacuous PASS.
#
# Two real defects this catches:
#   - task-list (opencode): transcript.jsonl + golden + expected.jsonl but NO
#     events.jsonl → ValidateExpected used to skip silently → fake PASS.
#   - foreground-subagent (opencode): a complete-looking recording whose dir
#     maps to NO by_adapter recipe (both recipe names absent) → orphaned from
#     any recipe, so it can never be --re-recorded.
#
# A cell is "recorded" (and thus checked) when its dir holds ANY of
# transcript.jsonl / transcript.md / events.jsonl. Recordings live under the
# recipe NAME dir (e.g. interrupted-turn), while assessment.json may live under
# the COVERAGE_ID dir (e.g. user-esc-interrupt) — the artifact set is per
# coverage_id, distributed across both. A complete recorded cell has:
#   recipe row  — the dir name is a by_adapter.<agent> recipe `name` OR a
#                 coverage_id whose recipes include one (else: orphan)
#   assessment.json — in the recording dir OR the coverage_id sibling dir
#   expected.jsonl, a transcript (.jsonl or .md), events.jsonl — in the dir
#   golden      — transcript.jsonl.replay.json.golden, REQUIRED for .jsonl cells
#                 (TestFixtureReplayByteIdentity pins it); .md cells have none.
#
# Sourced as a library (functions only; see cell-integrity_test.sh) AND runnable
# as a CLI. MUST NOT call `set` (it would leak options into a sourcing shell).
#
# #511: legitimate dir names + the dir→coverage_id mapping come from the
# per-scenario shards (replaydata/scenarios/), read through shard-lib.sh,
# instead of the retired scenarios.json.

# shellcheck source=shard-lib.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/shard-lib.sh"

# ci_recipe_dir_names <agent>
#   → every dir name an <agent> recording may legitimately use: each shard's
#     name (coverage_id) AND its recording_dir basename. A recorded dir NOT in
#     this set is an orphan (no shard to --re-record from).
ci_recipe_dir_names() {
  shard_recipe_dir_names "$1"
}

# ci_coverage_id_for_dir <dir-name> <agent>
#   → the coverage_id a recording dir belongs to: the shard whose name or
#     recording_dir basename == <dir-name>, else <dir-name> itself (already a
#     coverage_id). Used to find the sibling assessment.json dir.
ci_coverage_id_for_dir() {
  shard_coverage_for_dir "$1" "$2"
}

# ci_is_recorded <cell-dir>  → exit 0 iff the dir claims a recording.
# ci_is_recorded <cell-dir> → exit 0 iff the cell has at least one recording
#   under recordings/<name>/ (events.jsonl or a transcript).
ci_is_recorded() {
  local d r
  d="$1"
  for r in "$d"/recordings/*/; do
    [[ -d "$r" ]] || continue
    [[ -f "$r/transcript.jsonl" || -f "$r/transcript.md" || -f "$r/events.jsonl" ]] && return 0
  done
  return 1
}

# ci_missing_artifacts <agent> <dir-name> <cell-dir> <scenarios-root>
#   → prints each missing/inconsistent artifact, one per line. Returns 0 when
#     the cell is complete, 1 when at least one problem is printed. Every
#     recording lives under recordings/<name>/; expected.jsonl is the cell-root
#     spec. Each recording must be self-complete (events + transcript + golden).
ci_missing_artifacts() {
  local agent="$1" name="$2" dir="$3" sdir="$4"
  local problems=() r recname

  # recipe row — the folder must be a live cell (holds a metadata.json).
  if ! ci_recipe_dir_names "$agent" | grep -qxF "$name"; then
    problems+=("recipe-row [orphan: no $agent cell maps to '$name']")
  fi

  # assessment — lives in the cell's metadata.json (details.assessment), keyed
  # by coverage_id.
  local cid; cid="$(ci_coverage_id_for_dir "$name" "$agent")"
  if ! shard_has_assessment "$cid" "$agent"; then
    problems+=("assessment [absent from cell '$cid' details.assessment]")
  fi

  # The spec stays at the cell root.
  [[ -f "$dir/expected.jsonl" ]] || problems+=("expected.jsonl")

  # The NEWEST recording (the canonical one) must carry a complete set. Older
  # recordings are historical and may be partial — they're not gated (the
  # byte-identity golden test independently pins every transcript-bearing one).
  # Pathname globbing yields sorted-ascending names, so the last is the newest.
  local newest=""
  for r in "$dir"/recordings/*/; do [[ -d "$r" ]] && newest="$r"; done
  if [[ -n "$newest" ]]; then
    recname="$(basename "${newest%/}")"
    [[ -f "$newest/events.jsonl" ]] || problems+=("recordings/$recname/events.jsonl")
    if [[ -f "$newest/transcript.jsonl" ]]; then
      [[ -f "$newest/transcript.jsonl.replay.json.golden" ]] \
        || problems+=("recordings/$recname/transcript.jsonl.replay.json.golden")
    elif [[ ! -f "$newest/transcript.md" ]]; then
      problems+=("recordings/$recname/transcript.jsonl|transcript.md")
    fi
  fi

  [[ ${#problems[@]} -eq 0 ]] && return 0
  printf '%s\n' "${problems[@]}"
  return 1
}

# CLI: cell-integrity.sh [agent] [replaydata-root]
#   No agent → all five adapters. exit 0 = every recorded cell is complete;
#   exit 1 = at least one incomplete/orphaned cell (listed on stderr).
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  set -uo pipefail
  # repo root (shard-lib reads it): git toplevel, else up 4 from
  # tools/onboarding-factory/scripts/lib/.
  REPO_ROOT="$(git -C "$(dirname "${BASH_SOURCE[0]}")" rev-parse --show-toplevel 2>/dev/null)"
  [[ -n "$REPO_ROOT" ]] || REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"
  agent_arg="${1:-}"
  root="${2:-$REPO_ROOT/replaydata}"

  command -v jq >/dev/null || { echo "cell-integrity: jq is required" >&2; exit 2; }

  if [[ -n "$agent_arg" ]]; then agents=("$agent_arg"); else
    agents=(claudecode codex pi aider opencode); fi

  bad=0
  for agent in "${agents[@]}"; do
    sdir="$root/agents/$agent/scenarios"
    [[ -d "$sdir" ]] || continue
    echo "== cell-integrity: $agent =="
    for cell in "$sdir"/*/; do
      [[ -d "$cell" ]] || continue
      ci_is_recorded "$cell" || continue
      name="$(basename "$cell")"
      if probs="$(ci_missing_artifacts "$agent" "$name" "${cell%/}" "$sdir")"; then
        printf '  ok   %s\n' "$name"
      else
        printf '  BAD  %s — missing/inconsistent:\n' "$name" >&2
        while IFS= read -r p; do [[ -n "$p" ]] && printf '         - %s\n' "$p" >&2; done <<< "$probs"
        bad=$((bad + 1))
      fi
    done
  done

  echo ""
  if [[ "$bad" -eq 0 ]]; then
    echo "cell-integrity: every recorded cell carries a complete, consistent artifact set"
    exit 0
  fi
  echo "cell-integrity: $bad recorded cell(s) are incomplete or orphaned (see above)" >&2
  exit 1
fi
