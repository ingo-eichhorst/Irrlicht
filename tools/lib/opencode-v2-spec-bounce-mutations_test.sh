#!/usr/bin/env bash
# opencode-v2-spec-bounce-mutations_test.sh — committed mutation fixture for
# #2109 P5b-2's opencode-v2 2-* specs: a ready → working → ready bounce inside
# (or after) a turn must FAIL `of verify`, and the committed recording must
# PASS it.
#
# WHY THIS FILE EXISTS. P5b-2's review found the first specs blind to that
# shape: each turnN_done binds the FIRST ready after its start, so a 1 ms
# bounce at a step boundary satisfied every single-turn spec, and a ready inside
# one turn of 2-6 shifted the anchors instead of failing. The fix added, per
# cell, an unbounded "no state_transition to working" on the final ready (or
# error) phase, and a "one ready per turn" count on the multi-turn cells. Those
# are guards with no "before the fix" to run red, so each is mutated here: the
# recording itself is the subject, and a bounce is injected into a COPY of it.
#
#   ready-ending cells — working→ready at the session's last ready minus 1s,
#     then ready→working 1 ms later (a bounce inside the last turn);
#   error-ending cells (2-9, 2-14) — error→working 1 ms after the session's
#     error, then working→error 1 ms later (a bounce after the failure).
#
# Each cell is verified twice on a copy of replaydata/agents (adapters.json,
# scenarios.json, opencode-v2/): unchanged (must PASS) and injected (must
# FAIL). One more case rewrites 2-26's read-tool waiting into a write (the
# review's finding 3). Nothing in the worktree is written.

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
COL="replaydata/agents/opencode-v2/scenarios"

for tool in go jq python3; do
  command -v "$tool" >/dev/null 2>&1 || { echo "FAIL: opencode-v2-spec-bounce-mutations — $tool not found" >&2; exit 1; }
done

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
OF="$TMP/of"
(cd "$REPO_ROOT" && go build -o "$OF" ./tools/onboarding-factory/cmd/of) || { echo "FAIL: could not build of" >&2; exit 1; }

# The cells and how to bounce each one. Every recorded P5b-2 cell is listed:
# a cell whose recording disappears is a fixture that cannot run, not a pass.
CELLS="
2-2_auto-executed-tool-call:auto-executed-tool-call:ready
2-3_task-list:task-list:ready
2-4_self-correction-iteration:self-correction-iteration:ready
2-6_long-agentic-session-stress:long-agentic-session-stress:ready
2-10_mid-turn-message-queued:mid-turn-message-queued:ready
2-11_auto-classified-permission:auto-classified-permission:ready
2-13_turn-end-terminal-text:turn-end-terminal-text:ready
2-15_shell-escape-command:shell-escape-command:ready
2-21_streaming-partial-writes:streaming-partial-writes:ready
2-26_permission-gate-non-mutating-tool:permission-gate-non-mutating-tool:ready
2-9_token-quota-exhausted:token-quota-exhausted:error
2-14_turn-aborted-by-error:turn-aborted-by-error:error
"

# inject <events.jsonl> <ready|error> rewrites the file with the bounce added,
# in time order. It exits non-zero when the session has no such transition to
# anchor on — "could not inject" must never read as "the spec caught it".
inject() {
  python3 -I - "$1" "$2" <<'PY'
import json, re, sys
from datetime import datetime, timedelta
path, end = sys.argv[1], sys.argv[2]
def parse(ts):
    # Go writes RFC 3339 with trailing zeros trimmed (".59244"), which an older
    # datetime.fromisoformat refuses: pad the fraction to microseconds first.
    m = re.fullmatch(r"(.*T\d\d:\d\d:\d\d)(?:\.(\d+))?(Z|[+-]\d\d:\d\d)", ts)
    if not m:
        sys.exit("unparseable ts %r" % ts)
    frac = (m.group(2) or "").ljust(6, "0")[:6]
    return datetime.fromisoformat("%s.%s%s" % (m.group(1), frac, m.group(3).replace("Z", "+00:00")))
evs = [json.loads(l) for l in open(path) if l.strip()]
st = [e for e in evs if e.get("kind") == "state_transition" and str(e.get("session_id", "")).startswith("ses_")]
ends = [e for e in st if e.get("new_state") == end]
if not ends:
    sys.exit("no %s transition on a ses_ session to anchor the bounce on" % end)
ref = ends[-1]
t = parse(ref["ts"])
fmt = lambda d: d.isoformat(timespec="microseconds")
sid = ref["session_id"]
if end == "ready":
    pair = [(t - timedelta(seconds=1), "working", "ready"), (t - timedelta(seconds=1) + timedelta(milliseconds=1), "ready", "working")]
else:
    pair = [(t + timedelta(milliseconds=1), "error", "working"), (t + timedelta(milliseconds=2), "working", "error")]
for when, prev, new in pair:
    evs.append({"seq": 0, "ts": fmt(when), "kind": "state_transition", "session_id": sid, "prev_state": prev, "new_state": new, "reason": "injected bounce (mutation fixture)"})
evs.sort(key=lambda e: parse(e["ts"]))
with open(path, "w") as f:
    for e in evs:
        f.write(json.dumps(e) + "\n")
PY
}

fails=0
checked=0
for row in $CELLS; do
  folder="${row%%:*}"; rest="${row#*:}"; scenario="${rest%%:*}"; end="${rest#*:}"
  rec="$(ls -d "$REPO_ROOT/$COL/$folder/recordings/"*/ 2>/dev/null | sort | tail -1)"
  if [[ -z "$rec" || ! -s "$rec/events.jsonl" ]]; then
    echo "FAIL: $folder — no committed recording to mutate; this fixture cannot check the cell"
    fails=$((fails + 1)); continue
  fi
  copy="$TMP/repo-$folder"
  mkdir -p "$copy/replaydata/agents"
  cp "$REPO_ROOT/replaydata/agents/adapters.json" "$REPO_ROOT/replaydata/agents/scenarios.json" "$copy/replaydata/agents/"
  cp -R "$REPO_ROOT/replaydata/agents/opencode-v2" "$copy/replaydata/agents/"

  base="$("$OF" verify --agent opencode-v2 --scenario "$scenario" --repo-root "$copy" 2>&1)"
  if grep -q 'FAIL' <<<"$base" || ! grep -q 'state: *PASS' <<<"$base"; then
    echo "FAIL: $folder — the UNCHANGED recording does not pass its spec, so a red below would prove nothing"
    sed 's/^/      | /' <<<"$base"; fails=$((fails + 1)); continue
  fi

  copied="$copy/$COL/$folder/recordings/$(basename "$rec")/events.jsonl"
  if ! out="$(inject "$copied" "$end" 2>&1)"; then
    echo "FAIL: $folder — could not inject the bounce: $out"
    fails=$((fails + 1)); continue
  fi
  mut="$("$OF" verify --agent opencode-v2 --scenario "$scenario" --repo-root "$copy" 2>&1)"
  if grep -q 'FAIL' <<<"$mut"; then
    echo "ok  $folder — the injected bounce fails the spec ($(grep -m1 -oE '(state|events): *FAIL[^)]*' <<<"$mut" | head -1))"
    checked=$((checked + 1))
  else
    echo "FAIL: $folder — the spec stayed GREEN with a bounce injected ($end-ending)"
    sed 's/^/      | /' <<<"$mut"; fails=$((fails + 1))
  fi
done

# 2-26's point is that the READ tool's ask holds waiting. Its tool rides on the
# waiting transition's inputs, not on the hook line, so the spec pins it there:
# a turn 2 whose ask was another write must fail (P5b-2 review finding 3).
folder=2-26_permission-gate-non-mutating-tool
rec="$(ls -d "$REPO_ROOT/$COL/$folder/recordings/"*/ 2>/dev/null | sort | tail -1)"
copy="$TMP/repo-$folder-tool"
mkdir -p "$copy/replaydata/agents"
cp "$REPO_ROOT/replaydata/agents/adapters.json" "$REPO_ROOT/replaydata/agents/scenarios.json" "$copy/replaydata/agents/"
cp -R "$REPO_ROOT/replaydata/agents/opencode-v2" "$copy/replaydata/agents/"
ev="$copy/$COL/$folder/recordings/$(basename "$rec")/events.jsonl"
n="$(jq -c 'select(.kind=="state_transition" and .new_state=="waiting" and .inputs.last_open_tool_names[0]=="read")' "$ev" | grep -c .)"
if [[ "$n" != 1 ]]; then
  echo "FAIL: $folder — the recording has $n read-tool waiting transition(s), want 1; cannot mutate it"
  fails=$((fails + 1))
else
  jq -c 'if .kind=="state_transition" and .new_state=="waiting" and .inputs.last_open_tool_names[0]=="read" then .inputs.last_open_tool_names=["write"] else . end' "$ev" > "$ev.new" && mv "$ev.new" "$ev"
  mut="$("$OF" verify --agent opencode-v2 --scenario permission-gate-non-mutating-tool --repo-root "$copy" 2>&1)"
  if grep -qE 'events: *FAIL' <<<"$mut"; then
    echo "ok  $folder — a turn 2 that waited on write, not read, fails the spec"
    checked=$((checked + 1))
  else
    echo "FAIL: $folder — the spec stayed GREEN when turn 2's gate was a write"
    sed 's/^/      | /' <<<"$mut"; fails=$((fails + 1))
  fi
fi

if (( fails > 0 )); then
  echo "opencode-v2-spec-bounce-mutations: $fails FAILED"
  exit 1
fi
(( checked == 13 )) || { echo "opencode-v2-spec-bounce-mutations: checked $checked mutation(s), want 13"; exit 1; }
echo "opencode-v2-spec-bounce-mutations: ALL PASS ($checked mutations)"
