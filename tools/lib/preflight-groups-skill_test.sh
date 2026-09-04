#!/usr/bin/env bash
# preflight-groups-skill_test.sh — hold ir:exec SKILL.md's preflight `--only`
# chunk list against tools/preflight.sh's own VALID_GROUPS array (#1823).
#
# WHY THIS EXISTS. `.claude/skills/ir:exec/SKILL.md` tells
# every agent chunking `tools/preflight.sh` by `--only <group>` which groups
# to run. That list is hand-typed prose, not derived from anything — and it
# drifted: it named seven groups and silently omitted `swift` and `bash`,
# which `tools/preflight.sh` has accepted all along. An agent following only
# the skill on a `platforms/macos/` change skipped the whole macOS gate, and
# nothing said so — the skill's list and the real group set disagreed and
# both looked equally authoritative. A corrected hand-typed list fixes
# today's drift and guarantees a future one: the failure mode is the list
# itself, not any one omission from it.
#
# So this derives the canonical group set from `VALID_GROUPS` — the array at
# tools/preflight.sh's own `--only` argument-parsing site that actually
# DECIDES which values are accepted (an unrecognised one is refused, exit 2)
# — rather than from `--help`'s free-text Usage comment, which is a second,
# independently hand-maintained description of the same set with nothing
# tying it back to `VALID_GROUPS` either. Deriving from the Usage comment
# would only move #1823's failure mode one hop down the chain: a group added
# to `VALID_GROUPS` without a matching Usage line would still read as
# "canonical" and this lock would stay green while the real behavior drifted
# out from under it. `VALID_GROUPS` is the one place a wrong answer is
# actually unreachable — `--only` itself refuses anything not in it.
#
# Read as a literal via `sed`, not by sourcing tools/preflight.sh (which
# would run its whole argument-parsing/gate machinery) — the same technique
# tools/lib/module-list_test.sh already uses to read `WEB_TREES=(...)` out of
# tools/security-scan.sh without executing it.
#
# It compares against what the skill's own chunk-list code block actually
# names. `linux` is excluded on both sides: both AGENTS.md and the skill
# document it as deliberately opt-in (needs Docker) and carve it out of the
# chunk list in prose right next to it, so its absence from the list is a
# documented decision, not the drift this test exists to catch.
#
# This is a LOCK: it passes today by construction, because the fix above just
# made the two sides agree. Its value is that it CAN fail — proven by
# tools/lib/preflight-groups-skill-mutations_test.sh, which mutates one side
# with tools/mutate.sh and asserts this file goes red, then restores.
set -uo pipefail

REPO_ROOT=$(git rev-parse --show-toplevel)
# Every path below is repo-relative, so a failed cd would compare whatever
# happens to sit in the caller's cwd instead of the repo — most likely
# nothing on either side, which reads as a match. Exit instead (SC2164).
cd "$REPO_ROOT" || { echo "FAIL: cannot cd to repo root $REPO_ROOT" >&2; exit 1; }

SKILL_FILE=".claude/skills/ir:exec/SKILL.md"
PREFLIGHT="tools/preflight.sh"
rc=0
fail() { echo "FAIL: $1" >&2; rc=1; }

[ -f "$SKILL_FILE" ] || { echo "FAIL: preflight-groups-skill_test — $SKILL_FILE not found" >&2; exit 1; }
[ -f "$PREFLIGHT" ] || { echo "FAIL: preflight-groups-skill_test — $PREFLIGHT not found" >&2; exit 1; }

# --- derive the canonical group set from preflight.sh's own VALID_GROUPS --
all_groups=$(sed -n 's/^VALID_GROUPS=(\(.*\))$/\1/p' "$PREFLIGHT" | tr ' ' '\n' | sed '/^$/d' | sort -u)
if [ -z "$all_groups" ]; then
  echo "FAIL: preflight-groups-skill_test — could not read a 'VALID_GROUPS=(...)' array literal out of $PREFLIGHT; cannot derive the canonical group list" >&2
  exit 1
fi

# The `linux` exclusion is asserted, not assumed: if VALID_GROUPS ever stops
# naming a `linux` group (renamed, removed), filtering it below would
# silently do nothing and this test would start comparing against the WRONG
# canonical set without ever saying so — the same "exclusion stopped
# matching, gate stays green" shape shell-lib-suite.sh's skip-list
# validation guards.
if ! grep -qx 'linux' <<<"$all_groups"; then
  fail "expected $PREFLIGHT's VALID_GROUPS to list a 'linux' group to exclude (the skill documents omitting it deliberately); it does not — the exclusion is stale or was never valid"
fi
canonical_groups=$(printf '%s\n' "$all_groups" | grep -vx 'linux')

# --- extract the group list from SKILL.md's own chunk-list code block -----
# Anchored on the sentence introducing the fenced block, not on a line
# number — line numbers drift on every unrelated edit above this point.
skill_block=$(awk '
  /chunk: run each/ { armed = 1 }
  armed && /^[[:space:]]*```bash/ { infence = 1; next }
  infence && /^[[:space:]]*```/ { exit }
  infence { print }
' "$SKILL_FILE")

if [ -z "$skill_block" ]; then
  echo "FAIL: preflight-groups-skill_test — could not find the chunk-list code block in $SKILL_FILE (the anchor text may have moved); cannot verify anything" >&2
  exit 1
fi

skill_groups=$(printf '%s\n' "$skill_block" | grep -oE -- '--only [a-z]+' | awk '{print $2}' | sort -u)
if [ -z "$skill_groups" ]; then
  echo "FAIL: preflight-groups-skill_test — the chunk-list code block in $SKILL_FILE names no '--only <group>' line" >&2
  exit 1
fi

if [ "$canonical_groups" != "$skill_groups" ]; then
  fail "$SKILL_FILE's preflight chunk list disagrees with $PREFLIGHT's VALID_GROUPS (excluding the deliberately-omitted linux group)"
  diff <(echo "$canonical_groups") <(echo "$skill_groups") \
    | sed 's/^/  /; s/^  < /  only in VALID_GROUPS: /; s/^  > /  only in SKILL.md: /' >&2
fi

if [ "$rc" -eq 0 ]; then
  echo "OK: preflight-groups-skill_test — $SKILL_FILE's chunk list matches $PREFLIGHT's VALID_GROUPS ($(printf '%s' "$canonical_groups" | tr '\n' ' '))"
fi
exit "$rc"
