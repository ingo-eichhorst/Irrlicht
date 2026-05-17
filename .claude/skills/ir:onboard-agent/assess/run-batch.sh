#!/usr/bin/env bash
# run-batch.sh — orchestrate one batched assessment for the assess skill.
#
# The actual research (reading docs, changelog, source) is done by a subagent
# dispatched by the skill — this script only handles inputs and validation.
#
# Usage:
#   assess/run-batch.sh prepare  --column <agent>           # print column inputs (all scenarios of one agent)
#   assess/run-batch.sh prepare  --row    <scenario>        # print row inputs (all adapters of one scenario)
#   assess/run-batch.sh validate <candidate.json>           # schema + ID-set check (auto-detects column vs row)
#   assess/run-batch.sh commit   --column <agent>    <cand> # validate + write to .specs/agent-assess-<agent>.json
#   assess/run-batch.sh commit   --row    <scenario> <cand> # validate + write to .specs/scenario-assess-<scenario>.json

set -euo pipefail

SKILL_DIR="$(cd "$(dirname "$0")/.." && pwd)"
REPO_ROOT="$(cd "$SKILL_DIR/../../.." && pwd)"
ASSESS_DIR="$SKILL_DIR/assess"
COLUMN_SCHEMA="$ASSESS_DIR/schema/column.schema.json"
ROW_SCHEMA="$ASSESS_DIR/schema/row.schema.json"
COLUMN_PROMPT="$ASSESS_DIR/prompts/column.md"
ROW_PROMPT="$ASSESS_DIR/prompts/row.md"
CATALOG_MD="$REPO_ROOT/.specs/agent-scenarios.md"
CATALOG_JSON="$REPO_ROOT/.specs/agent-scenarios-coverage.json"

die() { echo "error: $*" >&2; exit 2; }

need_catalog() {
  [[ -f "$CATALOG_JSON" ]] || die "$CATALOG_JSON not found. Phase 0 needs the canonical scenario list. \
This file is gitignored and lives in the maintainer's checkout. \
Generate it from .specs/agent-scenarios.md or copy it from main."
  [[ -f "$CATALOG_MD" ]]   || die "$CATALOG_MD not found."
}

cmd_prepare() {
  local mode="${1:-}"; local target="${2:-}"
  [[ -n "$mode" && -n "$target" ]] || die "usage: run-batch.sh prepare --column <agent> | --row <scenario>"
  need_catalog

  case "$mode" in
    --column)
      echo "## Mode"
      echo "column — every scenario for agent \"$target\""
      echo
      echo "## Agent slug"
      echo "$target"
      echo
      echo "## Catalog paths"
      echo "- prose:    $CATALOG_MD"
      echo "- json IDs: $CATALOG_JSON"
      echo
      echo "## Scenario IDs to cover ($(jq '.scenarios | length' "$CATALOG_JSON") total)"
      jq -r '.scenarios[] | "- \(.id)  [\(.section)] \(.feature)"' "$CATALOG_JSON"
      echo
      echo "## Prompt"
      cat "$COLUMN_PROMPT"
      ;;
    --row)
      echo "## Mode"
      echo "row — every adapter for scenario \"$target\""
      echo
      echo "## Scenario ID"
      echo "$target"
      echo
      echo "## Catalog paths"
      echo "- prose:    $CATALOG_MD"
      echo "- json IDs: $CATALOG_JSON"
      echo
      local entry
      entry="$(jq -r --arg id "$target" '.scenarios[] | select(.id==$id) | "\(.id)  [\(.section)] \(.feature)"' "$CATALOG_JSON")"
      [[ -n "$entry" ]] || die "scenario id not in catalog: $target"
      echo "## Scenario"
      echo "$entry"
      echo
      echo "## Adapters to cover"
      jq -r '.agents[] | "- \(.id)"' "$CATALOG_JSON"
      echo
      echo "## Prompt"
      [[ -f "$ROW_PROMPT" ]] || die "$ROW_PROMPT not found (the row-mode prompt has not been authored yet)."
      cat "$ROW_PROMPT"
      ;;
    *) die "unknown mode: $mode (use --column or --row)" ;;
  esac
}

# Auto-detects column-vs-row by inspecting the candidate's top-level fields.
cmd_validate() {
  local cand="${1:-}"; [[ -n "$cand" && -f "$cand" ]] || die "usage: run-batch.sh validate <candidate.json>"
  need_catalog
  python3 - "$COLUMN_SCHEMA" "$ROW_SCHEMA" "$CATALOG_JSON" "$cand" <<'PY'
import json, sys, re, datetime
col_schema_path, row_schema_path, catalog_path, cand_path = sys.argv[1:5]
errs = []

def err(msg): errs.append(msg)

with open(catalog_path) as f: catalog = json.load(f)
with open(cand_path) as f:
    try: doc = json.load(f)
    except json.JSONDecodeError as e: print(f"candidate is not valid JSON: {e}", file=sys.stderr); sys.exit(1)

mode = None
if "scenarios" in doc and "agent" in doc: mode = "column"
elif "adapters" in doc and "scenario" in doc: mode = "row"
else: err("could not detect mode: expected either {agent, scenarios} (column) or {scenario, adapters} (row)")

valid_supports = {"yes","no","partial","unknown"}
valid_kinds = {"url","file","changelog","release_notes","issue","docs","source_code"}

def check_verdict(label, v):
    if not isinstance(v, dict): err(f"{label}: verdict not an object"); return
    for k in ("agent_supports","confidence","sources"):
        if k not in v: err(f"{label}: missing {k}")
    if v.get("agent_supports") not in valid_supports:
        err(f"{label}: agent_supports={v.get('agent_supports')!r} not in {sorted(valid_supports)}")
    c = v.get("confidence")
    if not (isinstance(c,(int,float)) and 0.0 <= c <= 1.0):
        err(f"{label}: confidence={c!r} not in 0..1")
    srcs = v.get("sources")
    if not isinstance(srcs, list) or len(srcs) < 1:
        err(f"{label}: sources must be a non-empty array")
    else:
        for i, s in enumerate(srcs):
            if not isinstance(s, dict): err(f"{label}.sources[{i}]: not an object"); continue
            if s.get("kind") not in valid_kinds:
                err(f"{label}.sources[{i}]: kind={s.get('kind')!r} not in {sorted(valid_kinds)}")
            if not s.get("ref"): err(f"{label}.sources[{i}]: ref must be non-empty")

if mode == "column":
    for k in ("schema_version", "agent", "agent_version", "surveyed_at", "scenarios"):
        if k not in doc: err(f"missing top-level key: {k}")
    if doc.get("schema_version") != 1: err(f"schema_version must be 1, got {doc.get('schema_version')!r}")
    if "agent" in doc and not re.fullmatch(r"[a-z][a-z0-9_-]*", doc["agent"]):
        err(f"agent slug invalid: {doc['agent']!r}")
    if "surveyed_at" in doc:
        try: datetime.datetime.fromisoformat(doc["surveyed_at"].replace("Z","+00:00"))
        except Exception as e: err(f"surveyed_at not ISO 8601: {e}")
    canonical = {s["id"] for s in catalog["scenarios"]}
    surveyed  = set(doc.get("scenarios", {}).keys())
    for s in sorted(surveyed - canonical): err(f"scenario id not in catalog: {s}")
    for s in sorted(canonical - surveyed): err(f"scenario id missing from column: {s}")
    for sid, v in (doc.get("scenarios") or {}).items():
        check_verdict(sid, v)
elif mode == "row":
    for k in ("schema_version", "scenario", "surveyed_at", "adapters"):
        if k not in doc: err(f"missing top-level key: {k}")
    if doc.get("schema_version") != 1: err(f"schema_version must be 1, got {doc.get('schema_version')!r}")
    if "scenario" in doc:
        if doc["scenario"] not in {s["id"] for s in catalog["scenarios"]}:
            err(f"scenario id not in catalog: {doc['scenario']!r}")
    if "surveyed_at" in doc:
        try: datetime.datetime.fromisoformat(doc["surveyed_at"].replace("Z","+00:00"))
        except Exception as e: err(f"surveyed_at not ISO 8601: {e}")
    canonical_adapters = {a["id"] for a in catalog.get("agents", [])}
    surveyed_adapters  = set(doc.get("adapters", {}).keys())
    for a in sorted(surveyed_adapters - canonical_adapters):
        err(f"adapter id not in catalog: {a}")
    # Row validation does NOT require every adapter to be present — some
    # may not be applicable (capabilities mismatch) and the verdict is implicit.
    for aid, v in (doc.get("adapters") or {}).items():
        check_verdict(aid, v)

if errs:
    print("VALIDATION FAILED:", file=sys.stderr)
    for e in errs: print(f"  - {e}", file=sys.stderr)
    sys.exit(1)

label = doc.get("agent") if mode == "column" else doc.get("scenario")
print(f"OK ({mode}): {cand_path} validates and covers expected ids for {label!r}.")
PY
}

cmd_commit() {
  local mode="${1:-}"; local target="${2:-}"; local cand="${3:-}"
  [[ -n "$mode" && -n "$target" && -n "$cand" && -f "$cand" ]] || \
    die "usage: run-batch.sh commit --column <agent> <candidate.json> | --row <scenario> <candidate.json>"
  cmd_validate "$cand"
  local dst
  case "$mode" in
    --column) dst="$REPO_ROOT/.specs/agent-assess-${target}.json" ;;
    --row)    dst="$REPO_ROOT/.specs/scenario-assess-${target}.json" ;;
    *) die "unknown mode: $mode (use --column or --row)" ;;
  esac
  mkdir -p "$(dirname "$dst")"
  if [[ -f "$dst" ]]; then cp "$dst" "$dst.bak"; echo "backed up prior version → $dst.bak" >&2; fi
  cp "$cand" "$dst"
  echo "wrote $dst" >&2
  echo
  echo "Maintainer next steps:"
  if [[ "$mode" == "--column" ]]; then
    echo "  1. Review verdicts: jq '.scenarios | to_entries[] | select(.value.confidence < 0.7)' $dst"
    echo "  2. Update .specs/agent-scenarios-coverage.json column for agent \"$target\" where verdict + confidence look right."
    echo "  3. Re-run column assess on agent version bump."
  else
    echo "  1. Review verdicts: jq '.adapters | to_entries[] | select(.value.confidence < 0.7)' $dst"
    echo "  2. Update .specs/agent-scenarios-coverage.json row for scenario \"$target\" where verdict + confidence look right."
    echo "  3. Re-run row assess when the scenario's prose spec changes."
  fi
}

case "${1:-}" in
  prepare)  shift; cmd_prepare  "$@" ;;
  validate) shift; cmd_validate "$@" ;;
  commit)   shift; cmd_commit   "$@" ;;
  -h|--help|"")
    sed -n '2,12p' "$0"; exit 0 ;;
  *) die "unknown subcommand: $1 (try: prepare | validate | commit)" ;;
esac
