#!/usr/bin/env bash
# antigravity-transcript-tail-census.sh reproduces the figure quoted in
# core/adapters/inbound/agents/antigravity/hooks.go: how many of this machine's
# real Antigravity conversations end on a parser-visible event that is NOT
# turn_done, and therefore leave a session pinned `working` when no Stop hook
# fires (issue #1723).
#
# It classifies each transcript's LAST parser-visible line exactly the way
# antigravity/parser.go's ParseLine dispatch does — SYSTEM steps are skipped, a
# MODEL/PLANNER_RESPONSE with no tool_calls is turn_done, one WITH tool_calls is
# assistant_message, any other MODEL step is a tool result, USER_INPUT is a user
# message.
#
# Read-only: it opens transcripts and writes nothing. It never starts a daemon
# and never runs `agy`.
#
# It REFUSES rather than printing a clean-looking zero when it cannot look —
# absent brain store, no conversation directories, no parseable transcript —
# because "nothing found" and "could not look" must not render identically.
#
# Usage:  bash tools/antigravity-transcript-tail-census.sh [brain-dir ...]
# Default: both declared brain stores (the CLI's and the IDE's).
set -euo pipefail

roots=()
if [[ "$#" -gt 0 ]]; then
	roots=("$@")
else
	roots=("$HOME/.gemini/antigravity-cli/brain" "$HOME/.gemini/antigravity/brain")
fi

present=()
for root in "${roots[@]}"; do
	if [[ -d "$root" ]]; then
		present+=("$root")
	fi
done

if [[ "${#present[@]}" -eq 0 ]]; then
	echo "REFUSING: none of the requested brain stores exists:" >&2
	printf '  %s\n' "${roots[@]}" >&2
	echo "This machine has no Antigravity conversations to census. That is not a census of zero." >&2
	exit 2
fi

python3 - "${present[@]}" <<'PY'
import json, os, sys, collections

roots = sys.argv[1:]
counts = collections.Counter()
dirs_seen = 0
transcripts = 0
unparseable = 0

for root in roots:
    for conv in sorted(os.listdir(root)):
        path = os.path.join(root, conv, ".system_generated", "logs", "transcript.jsonl")
        if not os.path.isdir(os.path.join(root, conv)):
            continue
        dirs_seen += 1
        if not os.path.isfile(path):
            continue
        last = None
        lines = 0
        with open(path, "r", errors="replace") as fh:
            for line in fh:
                line = line.strip()
                if not line:
                    continue
                lines += 1
                try:
                    obj = json.loads(line)
                except Exception:
                    continue
                src, typ = obj.get("source"), obj.get("type")
                if typ == "USER_INPUT":
                    kind = "user_message"
                elif src == "MODEL" and typ == "PLANNER_RESPONSE":
                    kind = "turn_done" if not obj.get("tool_calls") else "assistant_message"
                elif src == "MODEL":
                    kind = "function_call_output"
                else:
                    kind = None  # SYSTEM steps: parser.ParseLine sets Skip
                if kind:
                    last = kind
        if lines == 0:
            continue
        transcripts += 1
        if last is None:
            unparseable += 1
            continue
        counts[last] += 1

if dirs_seen == 0:
    sys.exit("REFUSING: the brain store(s) hold no conversation directories — nothing was censused.")
if transcripts == 0:
    sys.exit("REFUSING: %d conversation director(ies) hold no non-empty transcript.jsonl — nothing was censused."
             % dirs_seen)

total = sum(counts.values()) + unparseable
done = counts.get("turn_done", 0)
missing = total - done
print("roots            : %s" % ", ".join(roots))
print("transcripts read : %d (from %d conversation directories)" % (total, dirs_seen))
for kind, n in counts.most_common():
    print("  %-22s %d" % (kind, n))
if unparseable:
    print("  %-22s %d" % ("(no parsed event)", unparseable))
print("reaches turn_done: %d" % done)
print("does NOT         : %d  (%.1f%%)" % (missing, 100.0 * missing / total))
PY
