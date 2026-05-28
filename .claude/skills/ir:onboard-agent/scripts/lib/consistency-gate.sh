#!/usr/bin/env bash
# consistency-gate.sh — the assessment ⟺ scenarios agreement gate.
#
# WHY THIS EXISTS (the streaming-partial-writes desync, this session):
#   The completeness-gate classifies a cell from BOTH files but resolves any
#   disagreement SILENTLY. Its step-4 `recipe_false` branch marks a cell
#   `applicable_false` (terminal/green) the instant scenarios.json says
#   by_adapter.<agent>.applicable==false — WITHOUT ever checking that the
#   cell's assessment.json agrees. So an assessment can say "record this now"
#   (agent_supports yes/partial, daemon full/bug, driver ready) while the
#   matrix says applicable:false and nobody ever records it. That is exactly
#   how pi/streaming-partial-writes carried daemon=full/driver=ready (the
#   viewer showed it recordable) AND applicable:false (the gate showed it
#   terminal) at the same time — two files, two stories, every structural
#   gate green. A bulk schema migration (#480, 129 assessments) re-blessed the
#   stale optimistic verdict; the empirical degrade lived only in scenarios.json
#   prose. No gate caught the contradiction.
#
# This gate makes that disagreement a HARD failure. It is the SEMANTIC
# companion to catalog-drift.sh (which guards STRUCTURAL bijection): green
# here means "for every un-recorded cell, the assessment verdict and the
# matrix's applicable flag tell the same story."
#
# The routing matrix (assess/SKILL.md) is the single source of truth for what
# an assessment IMPLIES:
#   frozen      — agent_supports no|unknown, OR daemon_capability incapable|n/a
#                 → the matrix MUST mark this cell applicable:false.
#   record_now  — supports yes|partial AND daemon full|bug AND driver ready
#                 → the matrix MUST NOT mark it applicable:false (record it).
#   driver_gap  — driver gap:<primitive> (recordable after extend-driver)   } not
#   inconclusive— daemon unknown (re-assess before routing)                 } checked
#
# A committed fixture (transcript+events) under ANY of the cell's recipe-variant
# folders means the cell already recorded, so no disagreement matters — those
# are skipped. The unit is the coverage_id (mirroring completeness-gate), so a
# cell recorded under an alias folder (e.g. user-blocking-question recorded as
# agent-question-pending) is NOT falsely flagged.
#
# Sourced as a library (functions only; see consistency-gate_test.sh) AND
# runnable as a CLI. MUST NOT call `set` at top level (it would leak options
# into a sourcing shell). Reuses cg_candidate_dirs from completeness-gate.sh.

_CS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=completeness-gate.sh
source "$_CS_DIR/completeness-gate.sh"   # for cg_candidate_dirs (coverage_id → variant folders)

# cs_route <assessment.json> → frozen | record_now | driver_gap | inconclusive
# The assessment's routing class per the assess/SKILL.md verdict matrix.
cs_route() {
  jq -r '
    .agent_supports    as $s   |
    .daemon_capability as $d   |
    (.driver_capability // "")  as $drv |
    if   ($s=="no" or $s=="unknown") then "frozen"
    elif ($d=="incapable" or $d=="n/a" or $d=="n.a.") then "frozen"
    elif ($d=="unknown") then "inconclusive"
    elif ($drv|startswith("gap:")) then "driver_gap"
    else "record_now" end' "$1" 2>/dev/null
}

# cs_applicable_state <scenarios.json> <agent> <coverage_id> → absent | true | false
# Mirrors completeness-gate step 4: "false" only when EVERY by_adapter.<agent>
# recipe variant for the coverage_id is applicable:false; a single recordable
# variant makes the cell "true"; no variant at all → absent.
cs_applicable_state() {
  jq -r --arg a "$2" --arg cid "$3" '
    [ .scenarios[]
      | select(.coverage_id==$cid)
      | .by_adapter[$a] | select(. != null) | .applicable ] as $v
    | if   ($v|length)==0          then "absent"
      elif ($v|map(.==false)|all)  then "false"
      else "true" end' "$1" 2>/dev/null
}

# cs_cell_verdict <route> <applicable_state> <recorded:0|1> <record_blocked>
#   → "ok" | "CONTRADICTION_RECORD_NOW" | "CONTRADICTION_FROZEN"
# Pure decision table — the heart of the gate, isolated for unit testing.
#
# <record_blocked> is the assessment's documented reason a cell is NOT recorded
# even though its three axes say record-now (infra / unit_test / driver_bug /
# upstream — see assess/SKILL.md). A NON-EMPTY value makes a record_now ⟺
# applicable:false pairing CONSISTENT: the deferral is documented IN the
# assessment (so the viewer surfaces it), not buried in scenarios.json prose.
# An EMPTY value with that pairing is the silent desync this gate exists to
# catch (pi/streaming-partial-writes: axes said full/ready, no documented reason).
cs_cell_verdict() {
  local route="$1" appl="$2" recorded="$3" blocked="${4:-}"
  [[ "$recorded" == "1" ]] && { echo ok; return 0; }
  case "$route" in
    record_now)
      [[ "$appl" == "false" && -z "$blocked" ]] && { echo CONTRADICTION_RECORD_NOW; return 0; } ;;
    frozen)
      [[ "$appl" == "true" ]] && { echo CONTRADICTION_FROZEN; return 0; } ;;
  esac
  echo ok
}

# cs_coverage_id_for_folder <scenarios.json> <folder> → coverage_id
# A recipe-variant folder (name) maps to its coverage_id; a folder that is
# itself a catalog id maps to itself.
cs_coverage_id_for_folder() {
  jq -r --arg f "$2" '([ .scenarios[] | select(.name==$f) | .coverage_id ][0]) // $f' "$1" 2>/dev/null
}

# cs_errors <scenarios.json> <agents-root>
#   For every agent and every coverage_id APPLICABLE to it (capabilities +
#   requires_transport, via cg_applicable_coverage_ids — so transport-N/A
#   cells like opencode/oversized-transcript-line are correctly out of scope),
#   skips cells recorded under any variant folder, and prints one ERROR line
#   per contradiction. Returns 0 when clean else 1.
cs_errors() {
  local sj="$1" root="$2" rc=0
  local agent caps cid recorded assess d route appl blocked verdict supports daemon driver
  while IFS=$'\t' read -r agent cid; do
    [[ -z "$cid" ]] && continue
    recorded=0; assess=""
    while IFS= read -r d; do
      [[ -z "$d" ]] && continue
      if { [[ -f "$root/$agent/scenarios/$d/transcript.jsonl" || -f "$root/$agent/scenarios/$d/transcript.md" ]] \
           && [[ -f "$root/$agent/scenarios/$d/events.jsonl" ]]; }; then
        recorded=1
      fi
      [[ -z "$assess" && -f "$root/$agent/scenarios/$d/assessment.json" ]] && assess="$root/$agent/scenarios/$d/assessment.json"
    done < <(cg_candidate_dirs "$sj" "$cid")
    [[ "$recorded" == "1" ]] && continue
    [[ -n "$assess" ]] || continue
    route="$(cs_route "$assess")"
    [[ -z "$route" ]] && continue   # malformed/keyless → completeness-gate owns it
    appl="$(cs_applicable_state "$sj" "$agent" "$cid")"
    blocked="$(jq -r '.record_blocked // ""' "$assess" 2>/dev/null)"
    verdict="$(cs_cell_verdict "$route" "$appl" 0 "$blocked")"
    case "$verdict" in
      CONTRADICTION_RECORD_NOW)
        IFS=$'\t' read -r supports daemon driver < <(jq -r '[.agent_supports,.daemon_capability,.driver_capability]|@tsv' "$assess")
        echo "$agent/$cid: assessment routes RECORD (supports=$supports daemon=$daemon driver=$driver) but scenarios.json marks by_adapter.$agent applicable:false and no recording exists — reconcile: fix the assessment DOWN (e.g. daemon→incapable/unknown) or flip the matrix UP and record"
        rc=1 ;;
      CONTRADICTION_FROZEN)
        IFS=$'\t' read -r supports daemon driver < <(jq -r '[.agent_supports,.daemon_capability,.driver_capability]|@tsv' "$assess")
        echo "$agent/$cid: scenarios.json marks by_adapter.$agent applicable:true but the assessment routes FROZEN (supports=$supports daemon=$daemon) — reconcile: fix the assessment UP or mark the recipe applicable:false"
        rc=1 ;;
    esac
  done < <(
    for adir in "$root"/*/; do
      agent="$(basename "$adir")"
      caps="$adir/capabilities.json"
      [[ -f "$caps" ]] || continue
      while IFS= read -r cid; do
        [[ -n "$cid" ]] && printf '%s\t%s\n' "$agent" "$cid"
      done < <(cg_applicable_coverage_ids "$sj" "$caps")
    done | sort -u
  )
  return "$rc"
}

# CLI: consistency-gate.sh [scenarios.json] [agents-root]
#   exit 0 — assessment and scenarios agree on every un-recorded cell.
#   exit 1 — at least one contradiction (listed on stderr).
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  set -uo pipefail
  SK="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"   # …/ir:onboard-agent (lib → scripts → skill root)
  REPO="$(cd "$SK/../../.." && pwd)"
  sj="${1:-$SK/scenarios.json}"
  root="${2:-$REPO/replaydata/agents}"

  command -v jq >/dev/null || { echo "consistency-gate: jq is required" >&2; exit 2; }

  echo "== assessment ⟺ scenarios consistency gate ==" >&2
  if errs="$(cs_errors "$sj" "$root")"; then
    echo "  every un-recorded cell's assessment verdict agrees with its scenarios.json applicable flag" >&2
    echo "" >&2
    echo "consistency-gate: assessment and scenarios are consistent" >&2
    exit 0
  fi
  while IFS= read -r e; do [[ -n "$e" ]] && echo "  ERROR: $e" >&2; done <<< "$errs"
  echo "" >&2
  echo "consistency-gate: assessment ⟺ scenarios DISAGREE (see ERRORs above) — a cell's verdict and its matrix applicable flag tell different stories" >&2
  exit 1
fi
