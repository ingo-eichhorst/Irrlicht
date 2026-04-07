#!/usr/bin/env bash
# replay-fixtures.sh — run the offline replay against every fixture in
# testdata/issue-102/fixtures/ and emit JSON + Markdown reports next to them.
#
# Usage:
#   scripts/replay-fixtures.sh                       # default settings
#   scripts/replay-fixtures.sh --debounce 200ms      # tighter debounce window
#   scripts/replay-fixtures.sh --stale-tool 5s       # tighter stale-tool timeout
#
# Outputs (per fixture <name>.jsonl):
#   testdata/issue-102/reports/<name>.json   — full structured replay log
#   testdata/issue-102/reports/<name>.md     — human-readable summary
#
# The fixtures themselves are NOT committed to git (see .gitignore). Generate
# them by either copying transcripts from ~/.claude/projects manually or by
# running scripts/find-flicker-sessions.sh and picking candidates.

set -euo pipefail

DEBOUNCE="2s"
# Default to disabled — matches the Claude Code production policy after the
# fix for issue #102. Pass --stale-tool 15s to simulate the pre-fix behavior.
STALE_TOOL="0"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --debounce)   DEBOUNCE="$2"; shift 2 ;;
    --stale-tool) STALE_TOOL="$2"; shift 2 ;;
    -h|--help)
      sed -n '2,17p' "$0"
      exit 0
      ;;
    *) echo "unknown flag: $1" >&2; exit 2 ;;
  esac
done

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

FIXTURES_DIR="testdata/issue-102/fixtures"
REPORTS_DIR="testdata/issue-102/reports"

if [[ ! -d "$FIXTURES_DIR" ]]; then
  echo "no fixtures dir at $FIXTURES_DIR" >&2
  echo "create it and drop transcripts in, or run scripts/find-flicker-sessions.sh" >&2
  exit 1
fi

mkdir -p "$REPORTS_DIR" .build
BIN=".build/replay-session"
echo "building $BIN ..." >&2
( cd core && go build -o "../${BIN}" ./cmd/replay-session )

shopt -s nullglob
fixtures=("$FIXTURES_DIR"/*.jsonl)
if [[ ${#fixtures[@]} -eq 0 ]]; then
  echo "no .jsonl fixtures found in $FIXTURES_DIR" >&2
  exit 1
fi

for fix in "${fixtures[@]}"; do
  name="$(basename "${fix%.jsonl}")"
  json="$REPORTS_DIR/$name.json"
  md="$REPORTS_DIR/$name.md"

  echo ">> replaying $name" >&2
  "./$BIN" --out "$json" --debounce "$DEBOUNCE" --stale-tool "$STALE_TOOL" "$fix"

  python3 - "$json" "$md" "$fix" <<'PY'
import json, sys, os
from datetime import datetime, timezone

report_path, md_path, transcript = sys.argv[1], sys.argv[2], sys.argv[3]
with open(report_path) as f:
    r = json.load(f)
s = r["summary"]
settings = r["settings"]

def dur(ns):
    sec = ns / 1e9
    if sec < 60: return f"{sec:.1f}s"
    if sec < 3600: return f"{sec/60:.1f}m"
    return f"{sec/3600:.2f}h"

state_dur = s.get("state_durations") or {}
total_state = sum(state_dur.values()) or 1
def pct(s_name):
    return (state_dur.get(s_name, 0) / total_state) * 100

# Group transitions for the timeline table.
trs = r["transitions"]

with open(md_path, "w") as out:
    name = os.path.basename(transcript)
    out.write(f"# Replay report — {name}\n\n")
    out.write(f"_Source_: `{transcript}`\n\n")
    out.write(f"_Generated_: {r['generated_at']}\n\n")

    out.write("## Settings\n\n")
    out.write(f"- debounce window: `{dur(settings['debounce_window'])}`\n")
    out.write(f"- stale-tool timeout: `{dur(settings['stale_tool_timeout'])}`\n\n")

    out.write("## Summary\n\n")
    out.write(f"| metric | value |\n|---|---|\n")
    out.write(f"| total events | {s['total_events']} |\n")
    out.write(f"| consumed events (post-debounce) | {s['consumed_events']} |\n")
    out.write(f"| total transitions | {s['total_transitions']} |\n")
    out.write(f"| **flicker count** (all categories, short-lived sandwiches) | **{s['flicker_count']}** |\n")
    out.write(f"| stale-tool timer fires | {s['stale_timer_fires']} |\n")
    out.write(f"| first event | {s['first_event_time']} |\n")
    out.write(f"| last event | {s['last_event_time']} |\n")
    out.write(f"| session wall-clock | {dur(s['wall_clock_session_duration'])} |\n\n")

    cats = s.get("flickers_by_category") or {}
    if cats:
        out.write("### Flickers by category\n\n")
        out.write("| category | count |\n|---|---|\n")
        for k,v in sorted(cats.items(), key=lambda x: -x[1]):
            out.write(f"| {k} | {v} |\n")
        out.write("\n")
    reasons = s.get("flickers_by_reason") or {}
    if reasons:
        out.write("### Flickers by reason\n\n")
        out.write("| reason | count |\n|---|---|\n")
        for k,v in sorted(reasons.items(), key=lambda x: -x[1]):
            out.write(f"| {k} | {v} |\n")
        out.write("\n")

    out.write("### Time spent in each state\n\n")
    out.write("| state | duration | share |\n|---|---|---|\n")
    for st in ("working", "waiting", "ready"):
        d = state_dur.get(st, 0)
        out.write(f"| {st} | {dur(d)} | {pct(st):5.1f}% |\n")
    out.write("\n")

    # Flicker hot zones — find contiguous runs of flickers.
    flickers = []
    for i in range(1, len(trs)):
        a, b = trs[i-1]["new_state"], trs[i]["new_state"]
        if (a == "waiting" and b == "working") or (a == "working" and b == "waiting"):
            flickers.append(i)
    if flickers:
        out.write("## Flicker timeline\n\n")
        out.write("Each row is a state change involved in a flicker (the moment a\n")
        out.write("waiting↔working flip happened). `cause` distinguishes a real event\n")
        out.write("from a stale-tool timer firing inside a quiet window.\n\n")
        out.write("| # | virt. time | prev | new | cause | reason | open tools | last_event_type |\n")
        out.write("|---|---|---|---|---|---|---|---|\n")
        seen = set()
        for fi in flickers:
            for j in (fi-1, fi):
                if j in seen: continue
                seen.add(j)
                t = trs[j]
                tools = ",".join(t.get("open_tool_names") or []) or "—"
                out.write(
                    f"| {t['index']} | {t['virtual_time']} | {t['prev_state']} | {t['new_state']} | "
                    f"{t['cause']} | {t['reason']} | {tools} | {t['last_event_type']} |\n"
                )
        out.write("\n")

    out.write("## All transitions\n\n")
    out.write("| # | virt. time | prev → new | cause | reason |\n")
    out.write("|---|---|---|---|---|\n")
    for t in trs:
        out.write(
            f"| {t['index']} | {t['virtual_time']} | {t['prev_state'] or '∅'} → {t['new_state']} | "
            f"{t['cause']} | {t['reason']} |\n"
        )
PY

  echo "   wrote $json + $md" >&2
done

echo >&2
echo "done. reports in $REPORTS_DIR/" >&2
