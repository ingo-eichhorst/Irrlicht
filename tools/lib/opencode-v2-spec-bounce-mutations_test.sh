#!/usr/bin/env bash
# opencode-v2-spec-bounce-mutations_test.sh — committed mutation fixture for
# #2109 P5b-2's opencode-v2 2-* specs: a ready → working → ready bounce inside
# (or after) a turn must FAIL `of verify`, and the committed recording must
# PASS it.
#
# WHY THIS FILE EXISTS. P5b-2's review found the first specs blind to that
# shape: each turnN_done binds the FIRST ready after its start, so a 1 ms
# bounce at a step boundary satisfied every single-turn spec, a ready inside
# one turn of 2-6 shifted the anchors instead of failing, and the error-ending
# cells checked only what followed the error. The fix added an unbounded
# "no state_transition to working" on the final ready (or error) phase, a
# "one ready per turn" count on the multi-turn cells and on 2-9, and "no
# state_transition to ready" on 2-14's working phase. Those are guards with no
# "before the fix" to run red, so each is mutated here: the recording itself is
# the subject, and a bounce is injected into a COPY of it.
#
#   mid   — working→ready 1s before the session's last ready (or, with no
#           ready, its error), then ready→working 1 ms later: a bounce inside
#           the last turn that ends ready, or inside the failing turn;
#   after — error→working 1 ms after the session's error, then working→error
#           1 ms later (error-ending cells only).
#
# The cells are DERIVED, not typed: every opencode-v2 2-* cell with a cli-local
# recording, its end state read from the spec's last phase, so a new 2-* cell
# is checked the moment it is recorded. P5a's three smoke cells are excluded by
# name: their specs predate this guard and are not P5b-2's. One more case
# rewrites 2-26's read-tool waiting into a write (the review's finding 3).
#
# Each case runs `of verify --json` on its own copy of replaydata/agents
# (adapters.json, scenarios.json, opencode-v2/). Nothing in the worktree is
# written.

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
COL="replaydata/agents/opencode-v2/scenarios"
P5A_CELLS=" 2-1_basic-turn 2-19_tool-gate-permission-prompt 2-20_user-esc-interrupt "

for tool in go jq python3; do
  command -v "$tool" >/dev/null 2>&1 || { echo "FAIL: opencode-v2-spec-bounce-mutations — $tool not found" >&2; exit 1; }
done
# shellcheck source=../onboarding-factory/scripts/lib/recording-profile.sh
source "$REPO_ROOT/tools/onboarding-factory/scripts/lib/recording-profile.sh"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
OF="$TMP/of"
(cd "$REPO_ROOT" && go build -o "$OF" ./tools/onboarding-factory/cmd/of) || { echo "FAIL: could not build of" >&2; exit 1; }

# fresh_copy <name> prints a new copy of replaydata/agents for one case.
fresh_copy() {
  local copy="$TMP/$1"
  mkdir -p "$copy/replaydata/agents"
  cp "$REPO_ROOT/replaydata/agents/adapters.json" "$REPO_ROOT/replaydata/agents/scenarios.json" "$copy/replaydata/agents/"
  cp -R "$REPO_ROOT/replaydata/agents/opencode-v2" "$copy/replaydata/agents/"
  printf '%s\n' "$copy"
}

# verdict <scenario> <copy> prints "PASS" or the failing sections ("FAIL:state,events"),
# or "UNREADABLE" when of verify printed no report.
# (of verify exits non-zero on a failing report, so its status is not the
# verdict: the report's *_pass fields are.)
verdict() {
  local report
  report="$("$OF" verify --json --agent opencode-v2 --scenario "$1" --repo-root "$2" 2>/dev/null)"
  jq -r 'if has("state_pass") | not then empty else
           [to_entries[] | select((.key | endswith("_pass")) and .value == false) | .key | sub("_pass$"; "")]
           | if length == 0 then "PASS" else "FAIL:" + join(",") end end' <<<"$report" 2>/dev/null \
    | grep . || echo UNREADABLE
}

# inject <events.jsonl> <mid|after> rewrites the file with the bounce added, in
# time order. It exits non-zero when the session has no transition to anchor on
# — "could not inject" must never read as "the spec caught it".
inject() {
  python3 -I - "$1" "$2" <<'PY'
import json, re, sys
from datetime import datetime, timedelta
path, where = sys.argv[1], sys.argv[2]
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
readies = [e for e in st if e.get("new_state") == "ready"]
errors = [e for e in st if e.get("new_state") == "error"]
if where == "mid":
    ref = (readies or errors or [None])[-1]
    if ref is None:
        sys.exit("no ready or error transition on a ses_ session to anchor the bounce on")
    t = parse(ref["ts"]) - timedelta(seconds=1)
    pair = [(t, "working", "ready"), (t + timedelta(milliseconds=1), "ready", "working")]
else:
    if not errors:
        sys.exit("no error transition on a ses_ session to anchor the bounce on")
    ref = errors[-1]
    t = parse(ref["ts"])
    pair = [(t + timedelta(milliseconds=1), "error", "working"), (t + timedelta(milliseconds=2), "working", "error")]
for when, prev, new in pair:
    evs.append({"seq": 0, "ts": when.isoformat(timespec="microseconds"), "kind": "state_transition", "session_id": ref["session_id"],
                "prev_state": prev, "new_state": new, "reason": "injected bounce (mutation fixture)"})
evs.sort(key=lambda e: parse(e["ts"]))
with open(path, "w") as f:
    for e in evs:
        f.write(json.dumps(e) + "\n")
PY
}

fails=0 checked=0 want=0 cells=0
for cell_dir in "$REPO_ROOT/$COL"/2-*/; do
  folder="$(basename "$cell_dir")"
  [[ "$P5A_CELLS" == *" $folder "* ]] && continue
  rec="$(newest_recording_for_profile "${cell_dir%/}" cli-local)" || { echo "FAIL: $folder — could not read its recordings' profiles"; fails=$((fails + 1)); continue; }
  [[ -n "$rec" ]] || continue
  scenario="${folder#*_}"
  end="$(jq -rs '[.[] | select(.phase) | .expected_state // empty] | last // empty' "$cell_dir/expected.jsonl")"
  case "$end" in
    ready) modes="mid" ;;
    error) modes="mid after" ;;
    *) echo "FAIL: $folder — its spec's last phase ends in '$end', neither ready nor error"; fails=$((fails + 1)); continue ;;
  esac
  cells=$((cells + 1))

  base="$(verdict "$scenario" "$(fresh_copy "$folder-base")")"
  if [[ "$base" != PASS ]]; then
    echo "FAIL: $folder — the UNCHANGED recording does not pass its spec ($base), so a red below would prove nothing"
    fails=$((fails + 1)); continue
  fi
  for mode in $modes; do
    want=$((want + 1))
    copy="$(fresh_copy "$folder-$mode")"
    if ! out="$(inject "$copy/$COL/$folder/recordings/$(basename "$rec")/events.jsonl" "$mode" 2>&1)"; then
      echo "FAIL: $folder ($mode) — could not inject the bounce: $out"; fails=$((fails + 1)); continue
    fi
    got="$(verdict "$scenario" "$copy")"
    case "$got" in
      FAIL:*) echo "ok  $folder ($mode) — the injected bounce fails the spec ($got)"; checked=$((checked + 1)) ;;
      *) echo "FAIL: $folder ($mode) — the spec gave $got with a bounce injected"; fails=$((fails + 1)) ;;
    esac
  done
done

# 2-26's point is that the READ tool's ask holds waiting. Its tool rides on the
# waiting transition's inputs, not on the hook line, so the spec pins it there:
# a turn 2 whose ask was another write must fail (P5b-2 review finding 3).
folder=2-26_permission-gate-non-mutating-tool
want=$((want + 1))
rec="$(newest_recording_for_profile "$REPO_ROOT/$COL/$folder" cli-local)"
copy="$(fresh_copy "$folder-tool")"
ev="$copy/$COL/$folder/recordings/$(basename "$rec")/events.jsonl"
n="$(jq -c 'select(.kind=="state_transition" and .new_state=="waiting" and .inputs.last_open_tool_names[0]=="read")' "$ev" 2>/dev/null | grep -c .)"
if [[ -z "$rec" || "$n" != 1 ]]; then
  echo "FAIL: $folder — the recording has ${n:-no} read-tool waiting transition(s), want 1; cannot mutate it"
  fails=$((fails + 1))
else
  jq -c 'if .kind=="state_transition" and .new_state=="waiting" and .inputs.last_open_tool_names[0]=="read" then .inputs.last_open_tool_names=["write"] else . end' "$ev" > "$ev.new" && mv "$ev.new" "$ev"
  got="$(verdict permission-gate-non-mutating-tool "$copy")"
  if [[ "$got" == FAIL:*events* ]]; then
    echo "ok  $folder (tool) — a turn 2 that waited on write, not read, fails the spec ($got)"; checked=$((checked + 1))
  else
    echo "FAIL: $folder (tool) — the spec gave $got when turn 2's gate was a write"; fails=$((fails + 1))
  fi
fi

# A derivation that found (almost) nothing is a fixture that could not look.
(( cells >= 12 )) || { echo "FAIL: only $cells recorded 2-* cell(s) found under $COL — the derivation looked at nothing"; fails=$((fails + 1)); }
if (( fails > 0 || checked != want )); then
  echo "opencode-v2-spec-bounce-mutations: $fails FAILED ($checked of $want mutation(s) went red)"
  exit 1
fi
echo "opencode-v2-spec-bounce-mutations: ALL PASS ($checked mutations over $cells cells)"
