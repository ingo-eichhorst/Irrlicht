#!/usr/bin/env bash
# simplify-angle-guard_test.sh — hold ir:exec SKILL.md's step 14 to a
# mechanical requirement: it must instruct the agent to confirm all four of
# `/simplify`'s review angles reported back, and to fail LOUDLY (surface and
# pause) rather than silently when one didn't (#1823).
#
# WHY THIS EXISTS. `/simplify` is a built-in skill — `ls .claude/skills/`
# lists only `ir:*` skills, so it cannot be edited or instrumented from this
# repo. Its own fan-out silently dropped three of four review angles in a
# real run while the phase still reported success; nothing on our side can
# make the BUILT-IN itself fail loudly. The only lever this repo has is the
# instruction the calling skill gives the agent that invokes it — so the fix
# had to land in .claude/skills/ir:exec/SKILL.md's own step 14, not in
# `/simplify`.
#
# A prose fix on its own is exactly the failure this issue is about: none of
# the three instances #1823 reports were caught by review, because a
# verification step that silently stopped mattering reads identically to one
# that never needed to fire. So the requirement itself needs a mechanical
# check that can go stale-and-caught rather than stale-and-silent — this is
# that check. It is a LOCK (passes today by construction, since the fix above
# just added the text) proven capable of failing by
# tools/lib/simplify-angle-guard-mutations_test.sh, which deletes the
# requirement with tools/mutate.sh and confirms this goes red, then restores.
#
# What is (and isn't) checked. This cannot verify that a running agent
# actually reads `/simplify`'s report and confirms all four angles — no
# static check can enforce runtime behavior of a black-box built-in. What it
# CAN verify, and does: the instruction to do so is present, names all four
# angles by their real names (reuse, simplification, efficiency, altitude —
# `/simplify`'s own skill description uses the same four words), and pairs
# them with this skill's own established loud-failure idiom ("surface it and
# pause", already used for step 13's MISSING reviewer and step 9's failed
# self-assign) rather than a softer "be careful" phrasing that nothing
# enforces. That is the same class of guarantee tools/skill-lint.sh and
# tools/state-vocabulary-lint.sh already give this repo's other skill
# prose: not proof of compliance, but proof the instruction cannot silently
# erode out of the file the way the underlying defect did.
set -uo pipefail

REPO_ROOT=$(git rev-parse --show-toplevel)
cd "$REPO_ROOT" || { echo "FAIL: cannot cd to repo root $REPO_ROOT" >&2; exit 1; }

SKILL_FILE=".claude/skills/ir:exec/SKILL.md"
rc=0
fail() { echo "FAIL: $1" >&2; rc=1; }

[ -f "$SKILL_FILE" ] || { echo "FAIL: simplify-angle-guard_test — $SKILL_FILE not found" >&2; exit 1; }

# Anchored on step 14's own heading text through the next numbered step (14a)
# — not a line range, which drifts on every unrelated edit above this point.
step14=$(awk '
  /^14\. \*\*Simplify per the tier\.\*\*/ { armed = 1 }
  armed && /^14a\./ { exit }
  armed { print }
' "$SKILL_FILE")

if [ -z "$step14" ]; then
  echo "FAIL: simplify-angle-guard_test — could not find step 14 (the /simplify step) in $SKILL_FILE; its heading text may have moved. Cannot verify anything." >&2
  exit 1
fi

# Both checks below match against a whitespace-joined copy, not $step14
# directly: markdown's own line wrap can legitimately split either phrase
# across two source lines without changing what a reader sees rendered, and
# a literal multi-line grep would then report the phrase missing when it
# plainly isn't — the exact "absence of a finding and inability to look
# produce the same output" shape this whole issue is about, one layer down
# in this test itself. Computed once and reused by both checks below, since
# neither mutates $step14 in between.
step14_joined=$(printf '%s' "$step14" | tr '\n' ' ')

# All four angle names, together, in the enumeration that introduces the
# verification requirement — not a bare substring search for each word.
# Step 14 already said "reuse/simplification/efficiency/altitude" in its
# pre-#1823 Trivial/Small inline-review sentence (slash-separated), so a
# per-word `grep -qF "$angle"` would stay green even after the #1823
# verification sentence itself lost one — it would just be reading the OLD
# mention and calling it proof. The comma-separated form below is unique to
# the new sentence, so dropping an angle from THAT enumeration is what this
# must catch.
if ! grep -qF 'reuse, simplification, efficiency, altitude' <<<"$step14_joined"; then
  fail "$SKILL_FILE step 14 no longer enumerates all four angles (reuse, simplification, efficiency, altitude) together in its verification sentence — an agent reading only this step has no complete list to check /simplify's report against"
fi

# The loud-failure idiom itself: silence on an angle must read as a stop, not
# a caveat. "surface it and pause" is this skill's own established phrase for
# exactly this class of gate (step 9's failed self-assign, step 13's MISSING
# reviewer) — reusing it, rather than inventing new wording, is what keeps a
# future reader's grep for the idiom complete.
if ! grep -qF 'surface it and pause' <<<"$step14_joined"; then
  fail "$SKILL_FILE step 14 no longer pairs the four-angle check with a 'surface it and pause' instruction — a dropped angle would read as a caveat, not a stop"
fi

if [ "$rc" -eq 0 ]; then
  echo "OK: simplify-angle-guard_test — $SKILL_FILE step 14 names all four /simplify angles and requires surfacing a silent one"
fi
exit "$rc"
