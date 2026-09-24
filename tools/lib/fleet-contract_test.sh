#!/usr/bin/env bash
# Keep the ir:fleet orchestration contract and the ir:exec rules it depends on
# from drifting apart (#2022).
#
# WHY THIS EXISTS. The #2022 finding is that both rules the wave-1 fleet broke
# on 2026-09-20 were ALREADY written in ir:exec, so the fix is not more prose —
# it is a checker the orchestrator runs, plus a guard that keeps the
# instruction pointing at that checker. Each rule this file pins names an
# executable: delete the rule and a fleet agent is back to hand-rolling the
# idiom that failed. The behaviour of the checkers themselves is graded by
# tools/lib/ref-exists_test.sh, tools/lib/fleet-scope-overlap_test.sh and
# tools/lib/fleet-review-evidence_test.sh; this file grades the instructions.
set -uo pipefail # NOT -e: assertions capture non-zero return codes

REPO_ROOT=$(git rev-parse --show-toplevel)
cd "$REPO_ROOT" || { echo "FAIL: cannot cd to repo root $REPO_ROOT" >&2; exit 1; }

FLEET=.claude/skills/ir:fleet/SKILL.md
EXEC=.claude/skills/ir:exec/SKILL.md
rc=0
fail() { echo "FAIL: $1" >&2; rc=1; }

for file in "$FLEET" "$EXEC"; do
  [ -f "$file" ] || { echo "FAIL: $file not found" >&2; exit 1; }
done

# Every checker the two skills delegate a decision to must exist and be
# readable. A skill naming a script that is not there is the loudest possible
# form of this contract breaking.
for checker in tools/lib/ref-exists.sh tools/lib/fleet-scope-overlap.sh \
  tools/lib/fleet-review-evidence.sh; do
  [ -r "$checker" ] || fail "ir:fleet delegates to $checker, which is not readable"
  grep -qF -- "$checker" "$FLEET" || fail "ir:fleet no longer names $checker"
done

exec_wip=$(awk '
  /^## 2\. Create the worktree$/ { armed = 1 }
  armed && /^## 3\./ { exit }
  armed { print }
' "$EXEC")
exec_proof=$(awk '
  /^## 4\. Prove and verify$/ { armed = 1 }
  armed && /^## 5\./ { exit }
  armed { print }
' "$EXEC")
exec_review=$(awk '
  /^## 6\. Open and review the PR$/ { armed = 1 }
  armed && /^## 7\./ { exit }
  armed { print }
' "$EXEC")

if [ -z "$exec_wip" ] || [ -z "$exec_proof" ] || [ -z "$exec_review" ]; then
  echo "FAIL: could not find one of exec's sections 2, 4 or 6. Cannot verify anything." >&2
  exit 1
fi

# Newlines to spaces, so a literal may span a wrapped sentence.
wip_joined=$(printf '%s' "$exec_wip" | tr '\n' ' ')
proof_joined=$(printf '%s' "$exec_proof" | tr '\n' ' ')
review_joined=$(printf '%s' "$exec_review" | tr '\n' ' ')

# check <haystack-name> <haystack> <want> <message>
check() {
  local where="$1" haystack="$2" want="$3" message="$4"
  grep -qF -- "$want" <<<"$haystack" || fail "[$where] $message"
}

# ── ir:exec section 2 — a resume has an entry point ─────────────────────────
# Without one, the only way to resume is to tell an agent to skip the
# work-in-progress check, which is an instruction around the skill.
check wip "$wip_joined" 'names an existing worktree' \
  'exec section 2 no longer lets a caller resume an existing worktree; a fleet can only resume by instructing an agent to skip the check'

# ── ir:exec section 4 — the multi-run rules, each naming its instrument ─────
check proof "$proof_joined" 'never on a process name' \
  'exec section 4 no longer bans keying a wait on a process name'
check proof "$proof_joined" 'echo "EXIT=$?"; } > <own-log> 2>&1' \
  'exec section 4 no longer shows the redirect shape that puts the exit marker INSIDE the log'
check proof "$proof_joined" 'tools/lib/ref-exists.sh <remote> <branch>' \
  'exec section 4 no longer points the exact-ref check at tools/lib/ref-exists.sh'
check proof "$proof_joined" 'scratchpad filename the ticket number' \
  'exec section 4 no longer requires ticket-numbered scratchpad filenames'
check proof "$proof_joined" 'tools/preflight.sh --changed --only <group>' \
  'exec section 4 no longer names --changed, which is the mechanism that decides what a diff can break'

# ── ir:exec section 6 — a review is unproven until an instrument says so ────
check review "$review_joined" 'tools/lib/fleet-review-evidence.sh' \
  'exec section 6 no longer checks a delegated review with fleet-review-evidence.sh'
check review "$review_joined" 'is not a clean gate until there is evidence it' \
  'exec section 6 no longer says a silent review is not a clean gate'
check review "$review_joined" 'tools/lib/ref-exists.sh origin feat/<N>-<slug>' \
  'exec section 6 no longer confirms the push landed with an exact ref query'

# A substring ref check must not come back. `ls-remote | grep` is legitimate
# in exactly one place — exec section 2's work-in-progress search, where the
# branch slug is not yet known, so a pattern is the only option and the
# word-boundary form is safe against an object id (measured in
# tools/lib/ref-exists_test.sh). Every OTHER piped form is the idiom that told
# a wave-1 agent its push had landed when it had not.
for file in "$FLEET" "$EXEC"; do
  while IFS= read -r line; do
    [ -n "$line" ] || continue
    case "$line" in
      *'grep -w "<N>"'*) continue ;;
    esac
    fail "$file pipes git ls-remote into a grep that is not section 2's \`grep -w \"<N>\"\` pattern search: $line"
  done < <(grep -E 'ls-remote[^|]*\|[[:space:]]*grep' "$file")
done

fleet_body=$(cat "$FLEET")

# ── ir:fleet — the three answers it must never collapse ─────────────────────
check fleet "$fleet_body" 'UNDECLARED' \
  'ir:fleet no longer names the UNDECLARED scope verdict'
check fleet "$fleet_body" 'never co-dispatched' \
  'ir:fleet no longer keeps an undeclared scope out of a shared wave'
check fleet "$fleet_body" 'UNPROVEN' \
  'ir:fleet no longer names the UNPROVEN review verdict'
check fleet "$fleet_body" 'never reported as clean' \
  'ir:fleet no longer forbids reporting an unproven gate as clean'

# ── ir:fleet — the repo root it binds a spawned agent to ────────────────────
# Without the flag, --git-common-dir answers `.git` from the main checkout, so
# the brief told a spawned agent `Repo root: .` (#2045).
check fleet "$fleet_body" 'git rev-parse --path-format=absolute --git-common-dir' \
  'ir:fleet computes the repo root without --path-format=absolute, so it is relative from the main checkout'

# ── ir:fleet — the refusals ─────────────────────────────────────────────────
check fleet "$fleet_body" 'Explicit issue numbers only' \
  'ir:fleet no longer refuses a label or milestone expansion'
check fleet "$fleet_body" 'files a GitHub issue' \
  'ir:fleet no longer forbids the fleet from filing an issue'
check fleet "$fleet_body" 'Do not invoke the Workflow tool' \
  'ir:fleet no longer forbids the Workflow tool'

if grep -qF 'gh pr merge' "$FLEET"; then
  fail 'ir:fleet performs a merge'
fi

# The frontmatter name must equal the directory, or the skill loader cannot
# find it. tools/skill-lint.sh only warns about this; here it fails.
# sed, not `awk -F': *'`: the skill name CONTAINS a colon, and splitting on
# the separator turned `ir:fleet` into `ir`.
fm_name=$(sed -n 's/^name:[[:space:]]*//p' "$FLEET" | head -1)
[ "$fm_name" = "ir:fleet" ] ||
  fail "ir:fleet frontmatter name is '$fm_name', expected 'ir:fleet'"

if [ "$rc" -eq 0 ]; then
  echo 'OK: fleet-contract_test — every fleet rule still names the instrument that decides it'
fi
exit "$rc"
