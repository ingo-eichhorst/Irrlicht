#!/usr/bin/env bash
# run-pipeline.sh — Mode B orchestrator for #268 agent onboarding.
#
# Each stage is a subcommand; the full pipeline runs them in order via
# `run-pipeline.sh all <agent> <scenario>`. Use individual stages for
# debugging or re-runs.
#
# Usage:
#   run-pipeline.sh all      <agent> <scenario>
#   run-pipeline.sh probe    <agent>
#   run-pipeline.sh record   <agent> <scenario>
#   run-pipeline.sh label    <agent> <scenario>
#   run-pipeline.sh synth    <agent> <scenario>
#   run-pipeline.sh gen      <agent>
#   run-pipeline.sh validate <agent> <scenario>

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../../.." && pwd)"
STAGE_ROOT="$REPO_ROOT/.build/agent-onboarding/staged"
BIN="$REPO_ROOT/.build/agent-onboard"

die() { echo "error: $*" >&2; exit 2; }

build_bin() {
  if [[ ! -x "$BIN" ]]; then
    echo "building agent-onboard..." >&2
    ( cd "$REPO_ROOT" && go build -o "$BIN" ./tools/agent-onboarding/cmd/recorder )
  fi
}

scenario_dir() {
  local agent="$1" scenario="$2"
  echo "$STAGE_ROOT/$agent/$scenario"
}

cmd_probe() {
  local agent="${1:-}"; [[ -n "$agent" ]] || die "usage: probe <agent>"
  build_bin
  # The recorder's preflight check is currently inside record; we invoke
  # it dry by running record with --no-prereq-check OFF and no sensors.
  # In practice the maintainer touches .agent-onboarding/prereqs-<agent>.ok.
  local manifest="$REPO_ROOT/replaydata/agents/$agent/prerequisites.md"
  local ok="$REPO_ROOT/.agent-onboarding/prereqs-$agent.ok"
  if [[ -f "$manifest" && ! -f "$ok" ]]; then
    echo "prereqs not acknowledged for $agent" >&2
    echo "  manifest: $manifest" >&2
    echo "  expected: $ok" >&2
    echo "  fix:      touch $ok (after completing every item in the manifest)" >&2
    exit 1
  fi
  echo "probe OK: $agent" >&2
}

cmd_record() {
  local agent="${1:-}" scenario="${2:-}"
  [[ -n "$agent" && -n "$scenario" ]] || die "usage: record <agent> <scenario>"
  build_bin
  local out="$(scenario_dir "$agent" "$scenario")"
  mkdir -p "$out"
  echo "stage record is a no-op in this pipeline build — Phase 1's recorder is observer-only." >&2
  echo "The maintainer drives the agent under tmux + agent-onboard record by hand for now;" >&2
  echo "this stage will spawn the subprocess in a later Phase 6 iteration." >&2
  echo "Expected outputs under $out: signals.jsonl, events.jsonl, recording-meta.json" >&2
}

cmd_label() {
  local agent="${1:-}" scenario="${2:-}"
  [[ -n "$agent" && -n "$scenario" ]] || die "usage: label <agent> <scenario>"
  build_bin
  local dir="$(scenario_dir "$agent" "$scenario")"
  local sidecar="$dir/driver-sidecar.log"
  [[ -f "$sidecar" ]] || { echo "no sidecar at $sidecar — re-run record stage" >&2; exit 1; }
  "$BIN" label \
    --sidecar "$sidecar" \
    --out-dir "$dir" \
    --agent "$agent" \
    --scenario "$scenario"
}

cmd_synth() {
  local agent="${1:-}" scenario="${2:-}"
  [[ -n "$agent" && -n "$scenario" ]] || die "usage: synth <agent> <scenario>"
  build_bin
  local dir="$(scenario_dir "$agent" "$scenario")"
  local stage="$STAGE_ROOT/$agent"
  mkdir -p "$stage"
  "$BIN" synth \
    --agent "$agent" \
    --scenario "$scenario" \
    --signals "$dir/signals.jsonl" \
    --ground-truth "$dir/ground_truth.jsonl" \
    --staging "$STAGE_ROOT"
  local conflicts="$stage/synthesis_conflicts.json"
  if [[ -f "$conflicts" ]] && [[ "$(jq '.conflicts | length' "$conflicts")" != "0" ]]; then
    echo "synthesis_conflicts.json is non-empty — inspect $conflicts before continuing" >&2
    exit 1
  fi
  echo "synth OK: $stage/{ruleset,driver_protocol}.json" >&2
}

cmd_gen() {
  local agent="${1:-}"; [[ -n "$agent" ]] || die "usage: gen <agent>"
  build_bin
  local stage="$STAGE_ROOT/$agent"
  local gen_dir="$stage/generated"
  local adapter_out="$gen_dir/core/adapters/inbound/agents/$agent"
  local driver_out="$gen_dir/.claude/skills/ir:onboard-agent/scripts"
  rm -rf "$gen_dir"
  mkdir -p "$adapter_out" "$driver_out"
  "$BIN" gen \
    --agent "$agent" \
    --staging "$stage" \
    --adapter-out "$adapter_out" \
    --driver-out "$driver_out"
  echo "gen OK: staged adapter under $adapter_out" >&2
  echo "         staged driver  under $driver_out" >&2
  echo
  echo "Maintainer next steps:"
  echo "  cp -r $adapter_out/. core/adapters/inbound/agents/$agent/"
  echo "  cp    $driver_out/drive-$agent-interactive.sh .claude/skills/ir:onboard-agent/scripts/"
  echo "  # edit core/cmd/irrlichd/main.go:agentCfgs if first-time onboarding"
  echo "  go test ./core/... -race -count=1 && tools/replay-fixtures.sh"
}

cmd_validate() {
  local agent="${1:-}" scenario="${2:-}"
  [[ -n "$agent" && -n "$scenario" ]] || die "usage: validate <agent> <scenario>"
  build_bin
  local dir="$(scenario_dir "$agent" "$scenario")"
  "$BIN" validate \
    --agent "$agent" \
    --scenario "$scenario" \
    --events "$dir/events.jsonl" \
    --ground-truth "$dir/ground_truth.jsonl" \
    --out "$dir"
}

cmd_all() {
  local agent="${1:-}" scenario="${2:-}"
  [[ -n "$agent" && -n "$scenario" ]] || die "usage: all <agent> <scenario>"
  cmd_probe "$agent"
  cmd_record "$agent" "$scenario"
  cmd_label "$agent" "$scenario"
  cmd_synth "$agent" "$scenario"
  cmd_gen "$agent"
  cmd_validate "$agent" "$scenario"
}

case "${1:-}" in
  probe)    shift; cmd_probe    "$@" ;;
  record)   shift; cmd_record   "$@" ;;
  label)    shift; cmd_label    "$@" ;;
  synth)    shift; cmd_synth    "$@" ;;
  gen)      shift; cmd_gen      "$@" ;;
  validate) shift; cmd_validate "$@" ;;
  all)      shift; cmd_all      "$@" ;;
  -h|--help|"")
    sed -n '2,16p' "$0"; exit 0 ;;
  *) die "unknown stage: $1 (probe|record|label|synth|gen|validate|all)" ;;
esac
