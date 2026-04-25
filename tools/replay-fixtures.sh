#!/usr/bin/env bash
# replay-fixtures.sh — run the offline replay against every transcript in
# replaydata/agents/<adapter>/scenarios/<scenario>/transcript.{jsonl,md}
# and emit JSON + Markdown reports into replaydata/agents/_reports/.
# Aider fixtures use transcript.md (markdown source); other adapters use
# transcript.jsonl.
#
# Usage:
#   tools/replay-fixtures.sh                         # default settings
#   tools/replay-fixtures.sh --debounce 200ms        # tighter debounce window
#
# The replay binary auto-detects the adapter from the fixture path (claude
# code, codex, or pi).

set -euo pipefail

DEBOUNCE="2s"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --debounce)   DEBOUNCE="$2"; shift 2 ;;
    -h|--help)
      sed -n '2,11p' "$0"
      exit 0
      ;;
    *) echo "unknown flag: $1" >&2; exit 2 ;;
  esac
done

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

FIXTURES_ROOT="replaydata/agents"
REPORTS_DIR="replaydata/agents/_reports"

if [[ ! -d "$FIXTURES_ROOT" ]]; then
  echo "no fixtures root at $FIXTURES_ROOT" >&2
  exit 1
fi

mkdir -p "$REPORTS_DIR" .build
BIN=".build/replay"
echo "building $BIN ..." >&2
( cd core && go build -o "../${BIN}" ./cmd/replay )

# Walk every transcript.jsonl under replaydata/agents/<adapter>/scenarios/.
# Skip subagent transcripts (they live under .../subagents/ and are
# referenced, not replayed, by the parent's extended check). Portable
# to macOS bash 3.2 — avoid `mapfile`, use a while-read loop.
found_any=0
while IFS= read -r fix; do
  [[ -z "$fix" ]] && continue
  [[ "$fix" == */subagents/* ]] && continue
  found_any=1
  # Path shape: replaydata/agents/<adapter>/scenarios/<scenario>/transcript.jsonl
  scenario_dir="$(dirname "$fix")"
  name="$(basename "$scenario_dir")"
  adapter="$(basename "$(dirname "$(dirname "$scenario_dir")")")"
  json="$REPORTS_DIR/${adapter}-${name}.json"
  md="$REPORTS_DIR/${adapter}-${name}.md"

  echo ">> replaying ${adapter}/${name}" >&2
  "./$BIN" --out "$json" --debounce "$DEBOUNCE" "$fix"

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
    out.write(f"- debounce window: `{dur(settings['debounce_window'])}`\n\n")

    out.write("## Summary\n\n")
    out.write(f"| metric | value |\n|---|---|\n")
    out.write(f"| total events | {s['total_events']} |\n")
    out.write(f"| consumed events (post-debounce) | {s['consumed_events']} |\n")
    out.write(f"| total transitions | {s['total_transitions']} |\n")
    out.write(f"| **flicker count** (all categories, short-lived sandwiches) | **{s['flicker_count']}** |\n")
    out.write(f"| first event | {s['first_event_time']} |\n")
    out.write(f"| last event | {s['last_event_time']} |\n")
    out.write(f"| session wall-clock | {dur(s['wall_clock_session_duration'])} |\n")
    if s.get("estimated_cost_usd"):
        out.write(f"| **estimated cost** | **${s['estimated_cost_usd']:.4f}** |\n")
    if s.get("model_name"):
        out.write(f"| model | {s['model_name']} |\n")
    if s.get("cum_input_tokens"):
        out.write(f"| cumulative input tokens | {s['cum_input_tokens']:,} |\n")
    if s.get("cum_output_tokens"):
        out.write(f"| cumulative output tokens | {s['cum_output_tokens']:,} |\n")
    if s.get("cum_cache_read_tokens"):
        out.write(f"| cumulative cache read tokens | {s['cum_cache_read_tokens']:,} |\n")
    if s.get("cum_cache_creation_tokens"):
        out.write(f"| cumulative cache creation tokens | {s['cum_cache_creation_tokens']:,} |\n")
    out.write("\n")

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
        out.write("waiting↔working flip happened).\n\n")
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
done < <(find "$FIXTURES_ROOT" \( -name 'transcript.jsonl' -o -name 'transcript.md' \) -not -path '*/_reports/*' | sort)

if [[ "$found_any" -eq 0 ]]; then
  echo "no transcript fixtures found under $FIXTURES_ROOT/*/" >&2
  exit 1
fi

echo >&2
echo "done. reports in $REPORTS_DIR/" >&2
