#!/usr/bin/env bash
# completeness-gate.sh — post-sweep forcing function (#496 RC4): assert that
# every coverage_id APPLICABLE to an agent reached a TERMINAL verdict, so a
# cell can never silently fall through a sweep.
#
# The dispatcher used to enumerate sweep work from each subagent's lossy
# ≤5-line summary, so a cell the summary omitted (e.g. multiple-sessions-same-cwd:
# in scenarios.json, but no assessment + no recording) was never visited and
# "done" was reported anyway. This gate computes the work-list from the MATRIX
# FILES instead — scenarios.json (applicability via `requires` vs the agent's
# capabilities.json) crossed with the on-disk assessment/recording artifacts —
# and fails loudly when any applicable cell is non-terminal.
#
# Terminal verdicts (a cell that reached a decision):
#   recorded         — transcript.jsonl + events.jsonl committed
#   applicable_false — agent can't / daemon can't observe / degraded out
#   driver_gap       — recipe authored, awaiting an extend-driver step type
#                      (queued, owned work — terminal per #496 RC1)
# Non-terminal (the gate FAILS, listing them + the next action):
#   unassessed           — no assessment.json (never visited)
#   assessed_not_recorded — assessed record/record-known-failing but no recording
#
# Sourced as a library (functions only; see completeness-gate_test.sh) AND
# runnable as a CLI. This file MUST NOT call `set` (it would leak options into
# a sourcing shell).

# cg_applicable_coverage_ids <scenarios.json> <capabilities.json>
#   → applicable coverage_ids, one per line, sorted-unique. A coverage_id is
#     applicable iff every id in its scenarios' `requires` maps to `true` in
#     the agent's capabilities (false / "unknown" both block, matching the
#     matrix rule) AND, when a scenario declares `requires_transport`, the
#     agent's `transport` is in that list (#496 RC7). No `requires` ⇒
#     applicable; no `requires_transport` ⇒ any transport.
cg_applicable_coverage_ids() {
  local json="$1" caps="$2"
  # Applicability is computed PER recipe variant, then a coverage_id is
  # applicable iff ANY of its variants is. (Unioning requires across variants
  # and AND-ing would wrongly drop a cell whose one applicable variant has a
  # sibling needing an unmet feature/transport.)
  jq -r --slurpfile c "$caps" '
    (($c[0].features) // {}) as $f
    | (($c[0].transport) // "") as $t
    | [ .scenarios[]
        | {cid: .coverage_id, req: (.requires // []), tr: (.requires_transport // [])}
        | .applic = ((.req | all(. as $k | $f[$k] == true))
                     and (.tr | length == 0 or any(. == $t))) ]
    | group_by(.cid)
    | map(select(any(.[]; .applic)))
    | map(.[0].cid)
    | .[]
  ' "$json" 2>/dev/null | sort -u
}

# cg_candidate_dirs <scenarios.json> <coverage_id>
#   → the directory names a recording for this coverage_id may live under:
#     the coverage_id itself plus every recipe `name` mapping to it (a
#     coverage_id can have recipe variants, e.g. foreground-subagent ⇄
#     subagent-spawn). Sorted-unique.
cg_candidate_dirs() {
  local json="$1" cid="$2"
  { echo "$cid"
    jq -r --arg cid "$cid" '.scenarios[] | select(.coverage_id==$cid) | .name' \
      "$json" 2>/dev/null
  } | sed '/^$/d' | sort -u
}

# cg_disposition <scenarios.json> <agent> <coverage_id> <scenarios-dir>
#   → one word: recorded | applicable_false | driver_gap | assessed_not_recorded
#     | unassessed. <scenarios-dir> is replaydata/agents/<agent>/scenarios.
cg_disposition() {
  local json="$1" agent="$2" cid="$3" sdir="$4"
  local dir assess="" d

  # 1. recorded — any candidate dir with BOTH a transcript and events.
  while IFS= read -r d; do
    [[ -z "$d" ]] && continue
    if [[ -f "$sdir/$d/transcript.jsonl" && -f "$sdir/$d/events.jsonl" ]] \
       || [[ -f "$sdir/$d/transcript.md" && -f "$sdir/$d/events.jsonl" ]]; then
      echo recorded; return 0
    fi
    [[ -z "$assess" && -f "$sdir/$d/assessment.json" ]] && assess="$sdir/$d/assessment.json"
  done < <(cg_candidate_dirs "$json" "$cid")

  # 2. no assessment anywhere → never visited.
  [[ -n "$assess" ]] || { echo unassessed; return 0; }

  local supports daemon driver
  IFS=$'\t' read -r supports daemon driver < <(
    jq -r '[.agent_supports, .daemon_capability, .driver_capability] | @tsv' \
      "$assess" 2>/dev/null)

  # Malformed/keyless assessment.json → jq yields empty fields. Treat as
  # unassessed (re-assess) rather than letting it fall through to a misleading
  # assessed_not_recorded (which would say "implement" for a broken file).
  [[ -z "$supports$daemon$driver" ]] && { echo unassessed; return 0; }

  # 3. frozen by capability (agent can't, or daemon can't observe / inconclusive).
  case "$supports" in no|unknown) echo applicable_false; return 0 ;; esac
  case "$daemon" in incapable|n/a|"n/a") echo applicable_false; return 0 ;; esac

  # 4. degraded out at record time — ALL of the agent's recipe variants are
  #    applicable:false (none is recordable). `any` would wrongly freeze a cell
  #    that still has a recordable sibling variant.
  # NB: `.applicable // true` is WRONG here — jq's `//` treats `false` as empty,
  # so it would yield true for {applicable:false}. Test explicit `== false`.
  local recipe_false
  recipe_false="$(jq -r --arg cid "$cid" --arg a "$agent" '
    [ .scenarios[] | select(.coverage_id==$cid) | .by_adapter[$a]
      | select(. != null) | (.applicable == false) ] as $v
    | ($v | length) > 0 and ($v | all)' "$json" 2>/dev/null)"
  [[ "$recipe_false" == "true" ]] && { echo applicable_false; return 0; }

  # 5. driver gap — queued, owned work (recipe authored, awaiting extend-driver).
  case "$driver" in gap:*) echo driver_gap; return 0 ;; esac

  # 6. assessed as recordable (supports yes/partial, daemon full/bug, driver
  #    ready) but no recording landed — the cell the sweep must not drop.
  echo assessed_not_recorded
}

# CLI: completeness-gate.sh <agent> [scenarios.json] [replaydata-root]
#   exit 0 — every applicable coverage_id is terminal
#   exit 1 — one or more non-terminal cells (listed on stderr with next action)
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  set -uo pipefail
  agent="${1:?usage: completeness-gate.sh <agent> [scenarios.json] [replaydata-root]}"
  SK="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"          # …/ir:onboard-agent
  REPO="$(cd "$SK/../../.." && pwd)"                                 # repo root
  json="${2:-$SK/scenarios.json}"
  root="${3:-$REPO/replaydata}"
  caps="$root/agents/$agent/capabilities.json"
  sdir="$root/agents/$agent/scenarios"

  command -v jq >/dev/null || { echo "completeness-gate: jq is required" >&2; exit 2; }
  [[ -f "$caps" ]] || { echo "completeness-gate: no capabilities.json for '$agent' at $caps" >&2; exit 2; }

  non_terminal=0
  echo "== completeness gate: $agent =="
  while IFS= read -r cid; do
    [[ -z "$cid" ]] && continue
    disp="$(cg_disposition "$json" "$agent" "$cid" "$sdir")"
    case "$disp" in
      recorded|applicable_false|driver_gap)
        printf '  ok   %-32s %s\n' "$cid" "$disp" ;;
      unassessed)
        printf '  GAP  %-32s %s → assess %s %s\n' "$cid" "$disp" "$agent" "$cid" >&2
        non_terminal=$((non_terminal + 1)) ;;
      assessed_not_recorded)
        printf '  GAP  %-32s %s → implement %s %s\n' "$cid" "$disp" "$agent" "$cid" >&2
        non_terminal=$((non_terminal + 1)) ;;
    esac
  done < <(cg_applicable_coverage_ids "$json" "$caps")

  echo ""
  if [[ "$non_terminal" -eq 0 ]]; then
    echo "completeness-gate: $agent COMPLETE — every applicable cell is terminal"
    exit 0
  fi
  echo "completeness-gate: $agent INCOMPLETE — $non_terminal cell(s) did not reach a terminal verdict (see above)" >&2
  exit 1
fi
