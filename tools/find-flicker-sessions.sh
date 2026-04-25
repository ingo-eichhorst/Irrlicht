#!/usr/bin/env bash
# find-flicker-sessions.sh — scan local Claude Code transcripts and rank them
# by how much waiting↔working flicker the offline replay produces. Helps
# identify candidate fixtures for issue #102.
#
# Usage:
#   tools/find-flicker-sessions.sh [--limit N] [--min-events N]
#                                  [--projects-root DIR] [--out FILE]
#
# Defaults:
#   --limit         15        show top-N flickering sessions
#   --min-events    200       skip transcripts shorter than this
#   --projects-root ~/.claude/projects
#   --out           /dev/stdout
#
# The script builds the replay binary into .build/, then runs it
# against every transcript that meets the size threshold. Results are written
# as a tab-separated table sorted by flicker count desc.

set -euo pipefail

LIMIT=15
MIN_EVENTS=200
OUT="/dev/stdout"

# Canonical session-storage roots for each supported adapter. Missing
# directories are silently skipped so the script works on machines that
# don't use all three agents.
ROOTS=(
  "${HOME}/.claude/projects"
  "${HOME}/.codex/sessions"
  "${HOME}/.pi/agent/sessions"
)

while [[ $# -gt 0 ]]; do
  case "$1" in
    --limit)         LIMIT="$2"; shift 2 ;;
    --min-events)    MIN_EVENTS="$2"; shift 2 ;;
    --root)          ROOTS=("$2"); shift 2 ;;
    --out)           OUT="$2"; shift 2 ;;
    -h|--help)
      sed -n '2,18p' "$0"
      exit 0
      ;;
    *) echo "unknown flag: $1" >&2; exit 2 ;;
  esac
done

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

mkdir -p .build
BIN=".build/replay"
echo "building $BIN ..." >&2
( cd core && go build -o "../${BIN}" ./cmd/replay )

TMPDIR_REPORT="$(mktemp -d)"
trap 'rm -rf "$TMPDIR_REPORT"' EXIT

results_file="$TMPDIR_REPORT/results.tsv"
: >"$results_file"

count=0
for root in "${ROOTS[@]}"; do
  [[ -d "$root" ]] || continue
  while IFS= read -r -d '' transcript; do
    count=$((count + 1))

    # Skip subagent transcripts — they have a different lifecycle.
    if [[ "$transcript" == */subagents/* ]]; then continue; fi

    events=$(wc -l <"$transcript" | tr -d ' ')
    if (( events < MIN_EVENTS )); then continue; fi

    report="$TMPDIR_REPORT/report-${count}.json"
    if ! "./$BIN" --out "$report" --quiet "$transcript" 2>/dev/null; then
      continue
    fi

    # Pull summary fields with python (no jq dependency).
    python3 - "$report" "$transcript" >>"$results_file" <<'PY'
import json, sys
report_path, transcript = sys.argv[1], sys.argv[2]
with open(report_path) as f:
    r = json.load(f)
s = r["summary"]
print(
    "\t".join([
        str(s["flicker_count"]),
        str(s["total_transitions"]),
        str(s["total_events"]),
        r["settings"].get("adapter", "?"),
        s["first_event_time"],
        transcript,
    ])
)
PY
  done < <(find "$root" -name '*.jsonl' -print0)
done

# Sort by flicker count desc.
sort -t $'\t' -k1,1nr "$results_file" | head -n "$LIMIT" > "$TMPDIR_REPORT/top.tsv"

{
  printf "flickers\ttransitions\tevents\tadapter\tfirst_event\ttranscript\n"
  cat "$TMPDIR_REPORT/top.tsv"
} > "$OUT"
