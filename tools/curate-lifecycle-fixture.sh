#!/usr/bin/env bash
# curate-lifecycle-fixture.sh — copy a raw lifecycle recording + its
# transcript + any subagent transcripts into
# replaydata/agents/<adapter>/scenarios/<scenario>/ as a committable
# per-scenario fixture bundle.
#
# Filters the recording down to events for the named session and all
# of its file-based subagents (anything with parent_linked pointing at
# the parent), then copies the transcript verbatim along with every
# subagent .jsonl under <parent>/subagents/. The result is a
# self-contained fixture that lets a future replay tool reproduce the
# full parent-with-subagents lifecycle.
#
# Usage:
#   tools/curate-lifecycle-fixture.sh [-d <agents-root>] \
#     <recording.jsonl> <session-id> <transcript.jsonl> <adapter> <scenario>
#
# -d <agents-root> overrides the default ($REPO_ROOT/replaydata/agents).
# Used by the ir:onboard-agent skill to stage fixtures under .build/refresh/
# before a human reviews and copies them into the real replaydata/ tree.
#
# Example:
#   tools/curate-lifecycle-fixture.sh \
#     ~/.local/share/irrlicht/recordings/2026-04-11T153839-46b20d.jsonl \
#     b27fdaef-6de4-403a-b277-790fe8d803bb \
#     ~/.claude/projects/-Users-ingo-projects-irrlicht/b27fdaef-6de4-403a-b277-790fe8d803bb.jsonl \
#     claudecode \
#     11-background-agents-b27fdaef
#
# Writes:
#   <agents-root>/<adapter>/scenarios/<scenario>/transcript.jsonl
#       — parent transcript (unchanged)
#   <agents-root>/<adapter>/scenarios/<scenario>/events.jsonl
#       — lifecycle events for the parent AND every detected subagent,
#         sorted by seq
#   <agents-root>/<adapter>/scenarios/<scenario>/subagents/agent-*.jsonl
#       — each subagent transcript found under <parent>/subagents/

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
AGENTS_ROOT="$REPO_ROOT/replaydata/agents"

while [[ $# -gt 0 ]]; do
  case "$1" in
    -d)
      [[ $# -ge 2 ]] || { echo "-d requires an <agents-root> argument" >&2; exit 2; }
      AGENTS_ROOT="$2"
      shift 2
      ;;
    --)
      shift
      break
      ;;
    -h|--help)
      sed -n '2,35p' "$0"
      exit 0
      ;;
    -*)
      echo "unknown flag: $1" >&2
      exit 2
      ;;
    *)
      break
      ;;
  esac
done

if [[ $# -ne 5 ]]; then
  sed -n '2,35p' "$0"
  exit 2
fi

RECORDING="$1"
SESSION_ID="$2"
TRANSCRIPT="$3"
ADAPTER="$4"
SCENARIO="$5"

SCENARIO_DIR="$AGENTS_ROOT/$ADAPTER/scenarios/$SCENARIO"
mkdir -p "$SCENARIO_DIR"

if [[ ! -f "$RECORDING" ]]; then
  echo "recording not found: $RECORDING" >&2
  exit 1
fi
if [[ ! -f "$TRANSCRIPT" ]]; then
  echo "transcript not found: $TRANSCRIPT" >&2
  exit 1
fi

OUT_EVENTS="$SCENARIO_DIR/events.jsonl"
OUT_SUBAGENTS_DIR="$SCENARIO_DIR/subagents"

# Aider's source transcript is markdown (.aider.chat.history.md), not
# JSONL — preserve the native extension so the parser can ingest the file
# verbatim during replay.
if [[ "$ADAPTER" == "aider" ]]; then
  OUT_TRANSCRIPT="$SCENARIO_DIR/transcript.md"
else
  OUT_TRANSCRIPT="$SCENARIO_DIR/transcript.jsonl"
fi

# Discover child session IDs by scanning parent_linked events in the
# recording. Each such event carries (session_id=child, parent_session_id=parent).
CHILD_IDS=$(jq -rc --arg sid "$SESSION_ID" '
  select(.kind == "parent_linked" and .parent_session_id == $sid)
  | .session_id
' "$RECORDING" | sort -u)

CHILD_COUNT=$(printf '%s\n' "$CHILD_IDS" | grep -c . || true)

# Build a newline-delimited list of session IDs the filter should accept:
# the parent plus every discovered child.
SESSION_SET=$(printf '%s\n%s\n' "$SESSION_ID" "$CHILD_IDS" | grep -v '^$')

# Pull in pre-session events (proc-<pid>) whose pid matches a
# pid_discovered for any session in the set. The scanner emits
# presession_created/removed under session_id="proc-<pid>" before the
# real transcript arrives, so without this step the fixture would miss
# the detection window.
PROC_IDS=$(jq -rc --argjson ids "$(printf '%s\n' "$SESSION_SET" | jq -R . | jq -s .)" '
  select(.kind == "pid_discovered" and (.session_id as $s | $ids | index($s)))
  | "proc-\(.pid)"
' "$RECORDING" | sort -u)
PROC_COUNT=$(printf '%s\n' "$PROC_IDS" | grep -c . || true)
SESSION_SET=$(printf '%s\n%s\n' "$SESSION_SET" "$PROC_IDS" | grep -v '^$')

# Pass the set via an argfile-style jq variable so we don't exceed shell
# arg length limits when there are many subagents.
jq -c --argjson ids "$(printf '%s\n' "$SESSION_SET" | jq -R . | jq -s .)" '
  select(.session_id as $s | $ids | index($s))
' "$RECORDING" > "$OUT_EVENTS.unsorted"

# Re-sort by sequence number so the fixture events are in canonical order.
jq -s -c 'sort_by(.seq) | .[]' "$OUT_EVENTS.unsorted" > "$OUT_EVENTS"
rm -f "$OUT_EVENTS.unsorted"

EVENT_COUNT="$(wc -l < "$OUT_EVENTS" | tr -d ' ')"
if [[ "$EVENT_COUNT" -eq 0 ]]; then
  echo "no events matched session_id=$SESSION_ID (or its children) in $RECORDING" >&2
  rm -f "$OUT_EVENTS"
  exit 1
fi

cp "$TRANSCRIPT" "$OUT_TRANSCRIPT"

# Copy all subagent transcripts, if any. The real-world layout is
# <project-dir>/<parent-id>/subagents/<agent-id>.jsonl (Claude Code's
# subagent convention) — we mirror that flat in the fixture sibling dir.
REAL_SUBAGENTS_DIR="$(dirname "$TRANSCRIPT")/$SESSION_ID/subagents"
SUBAGENT_COUNT=0
if [[ -d "$REAL_SUBAGENTS_DIR" ]]; then
  rm -rf "$OUT_SUBAGENTS_DIR"
  mkdir -p "$OUT_SUBAGENTS_DIR"
  while IFS= read -r -d '' subfile; do
    cp "$subfile" "$OUT_SUBAGENTS_DIR/"
    SUBAGENT_COUNT=$((SUBAGENT_COUNT + 1))
  done < <(find "$REAL_SUBAGENTS_DIR" -maxdepth 1 -name '*.jsonl' -print0)
fi

echo "wrote $OUT_TRANSCRIPT ($(wc -l < "$OUT_TRANSCRIPT" | tr -d ' ') lines)"
echo "wrote $OUT_EVENTS ($EVENT_COUNT events from parent + $CHILD_COUNT children + $PROC_COUNT pre-sessions)"
if [[ "$SUBAGENT_COUNT" -gt 0 ]]; then
  echo "wrote $OUT_SUBAGENTS_DIR ($SUBAGENT_COUNT subagent transcripts)"
elif [[ "$CHILD_COUNT" -gt 0 ]]; then
  echo "note: $CHILD_COUNT children in recording but no transcripts found at $REAL_SUBAGENTS_DIR"
fi
