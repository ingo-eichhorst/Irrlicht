#!/usr/bin/env bash
# curate-lifecycle-fixture.sh — copy a raw lifecycle recording + its
# transcript into testdata/replay/<adapter>/ as a committable fixture pair.
#
# Filters the recording down to events for a single session ID, then
# copies the transcript verbatim. The committed pair is consumed by
# replay-session's extended-check mode when the user runs the fixture.
#
# Usage:
#   scripts/curate-lifecycle-fixture.sh \
#     <recording.jsonl> <session-id> <transcript.jsonl> <adapter> <fixture-name>
#
# Example:
#   scripts/curate-lifecycle-fixture.sh \
#     ~/.local/share/irrlicht/recordings/2026-04-11T114016.jsonl \
#     839f0678-4dbc-4aa5-a37c-10f12406d23f \
#     ~/.claude/projects/-Users-ingo-projects-irrlicht/839f0678-4dbc-4aa5-a37c-10f12406d23f.jsonl \
#     claudecode \
#     10-full-lifecycle-839f0678
#
# Writes:
#   testdata/replay/<adapter>/<fixture-name>.jsonl         (transcript)
#   testdata/replay/<adapter>/<fixture-name>.events.jsonl  (curated events)

set -euo pipefail

if [[ $# -ne 5 ]]; then
  sed -n '2,23p' "$0"
  exit 2
fi

RECORDING="$1"
SESSION_ID="$2"
TRANSCRIPT="$3"
ADAPTER="$4"
FIXTURE_NAME="$5"

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
FIXTURES_DIR="$REPO_ROOT/testdata/replay/$ADAPTER"

if [[ ! -f "$RECORDING" ]]; then
  echo "recording not found: $RECORDING" >&2
  exit 1
fi
if [[ ! -f "$TRANSCRIPT" ]]; then
  echo "transcript not found: $TRANSCRIPT" >&2
  exit 1
fi
if [[ ! -d "$FIXTURES_DIR" ]]; then
  echo "adapter fixtures dir not found: $FIXTURES_DIR" >&2
  exit 1
fi

OUT_TRANSCRIPT="$FIXTURES_DIR/${FIXTURE_NAME}.jsonl"
OUT_EVENTS="$FIXTURES_DIR/${FIXTURE_NAME}.events.jsonl"

# Filter events by session_id. jq -c keeps one object per line (JSONL).
jq -c --arg sid "$SESSION_ID" 'select(.session_id == $sid)' "$RECORDING" > "$OUT_EVENTS"

EVENT_COUNT="$(wc -l < "$OUT_EVENTS" | tr -d ' ')"
if [[ "$EVENT_COUNT" -eq 0 ]]; then
  echo "no events matched session_id=$SESSION_ID in $RECORDING" >&2
  rm -f "$OUT_EVENTS"
  exit 1
fi

cp "$TRANSCRIPT" "$OUT_TRANSCRIPT"

echo "wrote $OUT_TRANSCRIPT ($(wc -l < "$OUT_TRANSCRIPT" | tr -d ' ') lines)"
echo "wrote $OUT_EVENTS ($EVENT_COUNT events)"
