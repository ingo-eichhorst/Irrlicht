#!/usr/bin/env bash
# run-survey.sh — orchestrate one applicability survey of one agent.
#
# The actual research (reading docs, changelog, source) is done by a subagent
# dispatched by the skill — this script only handles inputs and validation.
#
# Usage:
#   survey/run-survey.sh prepare  <agent>             # print survey inputs to stdout
#   survey/run-survey.sh validate <candidate.json>    # schema + ID-set check
#   survey/run-survey.sh commit   <agent> <cand.json> # validate, then write to .specs/

set -euo pipefail

SKILL_DIR="$(cd "$(dirname "$0")/.." && pwd)"
REPO_ROOT="$(cd "$SKILL_DIR/../../.." && pwd)"
SURVEY_DIR="$SKILL_DIR/survey"
SCHEMA="$SURVEY_DIR/schema/survey-result.schema.json"
PROMPT="$SURVEY_DIR/prompts/applicability-survey.md"
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
  local agent="${1:-}"; [[ -n "$agent" ]] || die "usage: run-survey.sh prepare <agent>"
  need_catalog

  echo "## Agent slug"
  echo "$agent"
  echo
  echo "## Catalog paths"
  echo "- prose:    $CATALOG_MD"
  echo "- json IDs: $CATALOG_JSON"
  echo
  echo "## Scenario IDs to cover ($(jq '.scenarios | length' "$CATALOG_JSON") total)"
  jq -r '.scenarios[] | "- \(.id)  [\(.section)] \(.feature)"' "$CATALOG_JSON"
  echo
  echo "## Prompt"
  cat "$PROMPT"
}

# Schema + ID-set validation. Inline Python (no new dependency).
cmd_validate() {
  local cand="${1:-}"; [[ -n "$cand" && -f "$cand" ]] || die "usage: run-survey.sh validate <candidate.json>"
  need_catalog
  python3 - "$SCHEMA" "$CATALOG_JSON" "$cand" <<'PY'
import json, sys, re, datetime
schema_path, catalog_path, cand_path = sys.argv[1:4]
errs = []

def err(msg): errs.append(msg)

with open(schema_path) as f: schema = json.load(f)
with open(catalog_path) as f: catalog = json.load(f)
with open(cand_path) as f:
    try: doc = json.load(f)
    except json.JSONDecodeError as e: err(f"candidate is not valid JSON: {e}"); doc = None

if doc is not None:
    # Top-level shape.
    for k in ("schema_version", "agent", "agent_version", "surveyed_at", "scenarios"):
        if k not in doc: err(f"missing top-level key: {k}")
    if doc.get("schema_version") != 1: err(f"schema_version must be 1, got {doc.get('schema_version')!r}")
    if "agent" in doc and not re.fullmatch(r"[a-z][a-z0-9_-]*", doc["agent"]):
        err(f"agent slug invalid: {doc['agent']!r}")
    if "surveyed_at" in doc:
        try: datetime.datetime.fromisoformat(doc["surveyed_at"].replace("Z","+00:00"))
        except Exception as e: err(f"surveyed_at not ISO 8601: {e}")

    # Cross-reference scenarios against catalog.
    canonical = {s["id"] for s in catalog["scenarios"]}
    surveyed  = set(doc.get("scenarios", {}).keys())
    extra   = surveyed - canonical
    missing = canonical - surveyed
    for s in sorted(extra):   err(f"scenario id not in catalog: {s}")
    for s in sorted(missing): err(f"scenario id missing from survey: {s}")

    # Per-verdict shape.
    valid_supports = {"yes","no","partial","unknown"}
    valid_kinds = {"url","file","changelog","release_notes","issue","docs","source_code"}
    for sid, v in (doc.get("scenarios") or {}).items():
        if not isinstance(v, dict): err(f"{sid}: verdict not an object"); continue
        for k in ("agent_supports","confidence","sources"):
            if k not in v: err(f"{sid}: missing {k}")
        if v.get("agent_supports") not in valid_supports:
            err(f"{sid}: agent_supports={v.get('agent_supports')!r} not in {sorted(valid_supports)}")
        c = v.get("confidence")
        if not (isinstance(c,(int,float)) and 0.0 <= c <= 1.0):
            err(f"{sid}: confidence={c!r} not in 0..1")
        srcs = v.get("sources")
        if not isinstance(srcs, list) or len(srcs) < 1:
            err(f"{sid}: sources must be a non-empty array")
        else:
            for i, s in enumerate(srcs):
                if not isinstance(s, dict): err(f"{sid}.sources[{i}]: not an object"); continue
                if s.get("kind") not in valid_kinds:
                    err(f"{sid}.sources[{i}]: kind={s.get('kind')!r} not in {sorted(valid_kinds)}")
                if not s.get("ref"): err(f"{sid}.sources[{i}]: ref must be non-empty")

if errs:
    print("VALIDATION FAILED:", file=sys.stderr)
    for e in errs: print(f"  - {e}", file=sys.stderr)
    sys.exit(1)

print(f"OK: {cand_path} validates against {schema_path} and covers all {len(catalog['scenarios'])} catalog scenarios.")
PY
}

cmd_commit() {
  local agent="${1:-}"; local cand="${2:-}"
  [[ -n "$agent" && -n "$cand" && -f "$cand" ]] || die "usage: run-survey.sh commit <agent> <candidate.json>"
  cmd_validate "$cand"
  local dst="$REPO_ROOT/.specs/agent-survey-${agent}.json"
  mkdir -p "$(dirname "$dst")"
  if [[ -f "$dst" ]]; then cp "$dst" "$dst.bak"; echo "backed up prior version → $dst.bak" >&2; fi
  cp "$cand" "$dst"
  echo "wrote $dst" >&2
  echo
  echo "Maintainer next steps:"
  echo "  1. Review verdicts: jq '.scenarios | to_entries[] | select(.value.confidence < 0.7)' $dst"
  echo "  2. Update .specs/agent-scenarios-coverage.json cells for agent \"$agent\" where verdict + confidence look right."
  echo "  3. Re-run survey on agent version bump."
}

case "${1:-}" in
  prepare)  shift; cmd_prepare  "$@" ;;
  validate) shift; cmd_validate "$@" ;;
  commit)   shift; cmd_commit   "$@" ;;
  -h|--help|"")
    sed -n '2,11p' "$0"; exit 0 ;;
  *) die "unknown subcommand: $1 (try: prepare | validate | commit)" ;;
esac
