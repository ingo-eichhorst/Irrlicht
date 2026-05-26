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

# ci_recipe_dir_names <scenarios.json> <agent>
#   → every dir name that a by_adapter.<agent> recording may legitimately use:
#     the scenario `name`s AND `coverage_id`s of scenarios with a recipe. A
#     recorded dir NOT in this set is an orphan (no recipe to --re-record from).
ci_recipe_dir_names() {
  local json="$1" agent="$2"
  jq -r --arg a "$agent" '
    .scenarios[] | select(.by_adapter[$a] != null) | (.name, .coverage_id)
  ' "$json" 2>/dev/null | sort -u
}

# ci_coverage_id_for_dir <scenarios.json> <dir-name>
#   → the coverage_id a recording dir belongs to: the coverage_id of the
#     scenario whose `name` == <dir-name>, else <dir-name> itself (the dir is
#     already a coverage_id). Used to find the sibling assessment.json dir.
ci_coverage_id_for_dir() {
  local json="$1" name="$2" cid
  cid="$(jq -r --arg n "$name" '
    [ .scenarios[] | select(.name == $n) | .coverage_id ] | first // empty
  ' "$json" 2>/dev/null)"
  echo "${cid:-$name}"
}

# ci_is_recorded <cell-dir>  → exit 0 iff the dir claims a recording.
ci_is_recorded() {
  local d="$1"
  [[ -f "$d/transcript.jsonl" || -f "$d/transcript.md" || -f "$d/events.jsonl" ]]
}

# ci_missing_artifacts <scenarios.json> <agent> <dir-name> <cell-dir> <scenarios-root>
#   → prints each missing/inconsistent artifact, one per line. Returns 0 when
#     the cell is complete, 1 when at least one problem is printed.
ci_missing_artifacts() {
  local json="$1" agent="$2" name="$3" dir="$4" sdir="$5"
  local problems=()

  # recipe row — dir name must be a recipe name (or its coverage_id).
  if ! ci_recipe_dir_names "$json" "$agent" | grep -qxF "$name"; then
    problems+=("recipe-row [orphan: no by_adapter.$agent recipe maps to '$name']")
  fi

  # assessment.json — per coverage_id; accept it here OR in the coverage_id dir.
  local cid; cid="$(ci_coverage_id_for_dir "$json" "$name")"
  if [[ ! -f "$dir/assessment.json" && ! -f "$sdir/$cid/assessment.json" ]]; then
    problems+=("assessment.json [absent in this dir and in coverage_id dir '$cid']")
  fi

  [[ -f "$dir/expected.jsonl" ]] || problems+=("expected.jsonl")
  [[ -f "$dir/events.jsonl" ]]   || problems+=("events.jsonl")

  if [[ -f "$dir/transcript.jsonl" ]]; then
    [[ -f "$dir/transcript.jsonl.replay.json.golden" ]] \
      || problems+=("transcript.jsonl.replay.json.golden")
  elif [[ ! -f "$dir/transcript.md" ]]; then
    problems+=("transcript.jsonl|transcript.md")
  fi

  [[ ${#problems[@]} -eq 0 ]] && return 0
  printf '%s\n' "${problems[@]}"
  return 1
}

# CLI: cell-integrity.sh [agent] [scenarios.json] [replaydata-root]
#   No agent → all five adapters. exit 0 = every recorded cell is complete;
#   exit 1 = at least one incomplete/orphaned cell (listed on stderr).
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  set -uo pipefail
  SK="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"          # …/ir:onboard-agent
  REPO="$(cd "$SK/../../.." && pwd)"                                 # repo root
  agent_arg="${1:-}"
  json="${2:-$SK/scenarios.json}"
  root="${3:-$REPO/replaydata}"

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
      if probs="$(ci_missing_artifacts "$json" "$agent" "$name" "${cell%/}" "$sdir")"; then
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
