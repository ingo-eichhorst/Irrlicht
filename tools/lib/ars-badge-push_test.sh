#!/usr/bin/env bash
# ars-badge-push_test.sh — the lock on .github/workflows/ars.yml's badge job.
#
# The filename predates its scope. It was written for the "Commit badge update"
# step (#1641) and grew to cover "Run ARS scan" and "Extract and update ARS
# badge" (#1644) — the two steps that PRODUCE the badge that step pushes. One
# harness per workflow rather than one per step, because the three share a
# workflow file, a `workflow_step_shell` derivation, an assertion vocabulary
# and one preflight trigger; splitting them would duplicate all four and let
# the copies disagree, which is what tools/lib/workflow-step.sh exists to stop.
# Renaming the file was declined as pure churn against five commits of history.
#
# ---------------------------------------------------------------------------
# What is being asserted, and why each obligation exists
#
# The defect: the step's push-retry loop was
#
#     for i in 1 2 3 4 5; do
#       git pull --rebase origin main && git push && break
#       echo "Push attempt $i failed, retrying..."
#       sleep $((i * 3))
#     done
#
# The loop's last statement is `sleep`, which succeeds. So when all five
# attempts failed the loop simply ended, `sleep`'s 0 became the step's status,
# and the step, the job and the badge check all went GREEN with the badge
# unpushed. GitHub's implicit `-e` does not save it: `A && B && break` is an
# `&&` list, and errexit is suppressed for every command in such a list except
# the one following the final `&&` — which is exactly what MAKES this a retry
# rather than a single attempt. The suppression is wanted; what was missing is
# anything that then READS the exhausted-retries case.
#
# The obligations, in order:
#
#   1. the defect is re-MEASURED, not described. The pre-fix loop is emitted
#      verbatim and run under the shell GitHub actually uses, so "five failures
#      exit 0" is a fact on every run rather than a sentence in a merged PR
#      body. It doubles as the vacuity guard: if bash ever stopped behaving
#      that way, the fix would be protecting nothing and every arm below would
#      pass for the wrong reason.
#   2. the real step — extracted from the real workflow file and EXECUTED —
#      fails when every attempt fails, and says what did not happen. This is
#      deliberately behavioural rather than a text scan: a scan pins one
#      spelling of a guard, where running the block pins the property.
#   3. the retry is still a retry. A "fix" that dropped the loop, or that
#      stopped suppressing errexit inside the `&&` list, also satisfies
#      obligation 2 — it just gives up after one attempt. So a run whose third
#      attempt succeeds must exit 0 AND show that the first two were retried.
#   4. the clean paths still pass: nothing staged, and a first-attempt push.
#
# Obligations 12-17 are #1655's, on the same loop and against REAL git in a
# throwaway repo, because the defect there is STATE rather than status — a
# rebase that conflicts leaves the tree mid-rebase and the stub above can no
# more produce that than it can undo it. Their header sits with them.
#
# ---------------------------------------------------------------------------
# #1644 — the two steps ABOVE it, same job, same symptom
#
# The badge that step pushes is produced by "Run ARS scan" and "Extract and
# update ARS badge", and both used to answer "I could not look" with the same
# bytes as "there was nothing to do":
#
#     ars scan ./core --no-llm --badge > ars-output.txt 2>&1 || true
#     cat ars-output.txt
#
# `|| true` swallows any failure and the block's last statement is `cat`, which
# succeeds. `ars-output.txt` then holds an error, the extract step's `grep`
# matches nothing, its `if [ -n "$ARS_BADGE" ]` is skipped in silence, README is
# untouched, and "Commit badge update" reports `No badge changes to commit`.
# Three green steps and a badge frozen at its previous score.
#
# The obligations added for it:
#
#   5. the defect is re-MEASURED, not described: both pre-#1644 bodies are
#      emitted verbatim and run against a failing `ars`, and both still exit 0
#      with README unchanged. The permanent vacuity guard for 6-11 — if that
#      ever stopped reproducing, they would all pass for the wrong reason.
#   6. a FAILED scan is no longer green, and the step says the badge was not
#      updated. The scan's own output is still printed, on both paths, or the
#      failure has no diagnosis in the log.
#   7. a scan that SUCCEEDS is still green (the vacuity guard for 6 — a step
#      that failed unconditionally would satisfy 6 perfectly).
#   8. a NORMAL run still rewrites the badge: the real README.md, the real
#      extract body, a new score in, the new URL in the file and the old one
#      gone. The vacuity guard for 9-11, and the only arm that would notice the
#      whole step being replaced by `exit 1`.
#   9. output with NO badge line is a named failure — the third outcome the
#      issue is about, since it means `--badge` stopped emitting one rather
#      than that the score was unchanged.
#  10. a badge line carrying no extractable URL is its OWN named failure.
#  11. a rewrite that did not land in README.md is its OWN named failure. `sed`
#      exits 0 having matched nothing, so this is the same defect one level
#      down, and re-reading the file for the URL just written is what
#      distinguishes it from the legitimate no-op of an unchanged score.
#
#  9-11 additionally assert that each refusal does NOT print the other two's
#  wording. Three refusals that all fire together, or all print the same
#  sentence, would satisfy every arm above while leaving the operator exactly
#  where the `if` skips left them: a red step and no idea which thing broke.
#
# Deliberately NOT a general workflow linter, and the measurement is the
# honest part. Over this repo's 19 multi-line `run:` blocks, a rule keyed on
# "the block's last statement is echo/sleep/cat/printf" flags 6 and MISSES
# THIS ONE — ars.yml's retry loop is nested inside an if/else, so the block's
# last line is `fi` — while 4 of the 6 it does flag are correct code. A rule
# keyed on `|| true` flags 2 and also misses this one. A rule keyed on "the
# block contains a loop" flags 3, of which 1 is this defect. No candidate rule
# both catches the subject and stays quiet on the correct blocks, so a
# linter's green would claim coverage it does not have. This is a lock on the
# ONE call site that exists — the same conclusion, reached the same way, as
# #1629's scan in swift-suite_test.sh.
set -uo pipefail

REPO_ROOT=$(git rev-parse --show-toplevel)
cd "$REPO_ROOT" || { echo "FAIL: ars-badge-push_test — cannot cd to $REPO_ROOT" >&2; exit 1; }

# A missing tool is a hard failure, not a skip: exiting 0 here would read as a
# PASS to the runner that drives this file, so it would go green having
# asserted nothing — the failure mode this whole issue family is made of.
need() { command -v "$1" >/dev/null 2>&1 || { echo "FAIL: ars-badge-push_test — $1 not found" >&2; exit 1; }; }
need git
need mktemp
need awk
need sed
need grep
need bash

WF=.github/workflows/ars.yml
STEP='Commit badge update'
SCAN_STEP='Run ARS scan'
EXTRACT_STEP='Extract and update ARS badge'

# The step's body AND the shell it runs under both come from the workflow file,
# through tools/lib/workflow-step.sh — see the invocation block below.
# shellcheck source=workflow-step.sh
. tools/lib/workflow-step.sh

TMP=$(mktemp -d -t ars-badge-push) || exit 1
trap 'rm -rf "$TMP"' EXIT

rc=0
pass() { echo "  PASS: $1"; return 0; }
fail() { echo "  FAIL: $1 — expected [$2] got [$3]" >&2; rc=1; return 0; }
flat() { echo "$1" | tr '\n' ' '; }

want_status() { # label want got out
  if [[ "$3" == "$2" ]]; then pass "$1 (exit $2)"; else fail "$1" "exit $2" "exit $3 :: $(flat "$4")"; fi
  return 0
}
want_contains() { # label needle haystack
  case "$3" in *"$2"*) pass "$1" ;; *) fail "$1" "output containing: $2" "$(flat "$3")" ;; esac
  return 0
}
want_absent() { # label needle haystack
  case "$3" in *"$2"*) fail "$1" "no output containing: $2" "$(flat "$3")" ;; *) pass "$1" ;; esac
  return 0
}

# ---------------------------------------------------------------------------
# The invocation, DERIVED from the workflow rather than spelled here (#1650).
#
# GitHub has two bash invocations and this file used to state the wrong one as
# fact: a step DECLARING `shell: bash` gets
# `bash --noprofile --norc -e -o pipefail {0}`, but a step declaring nothing —
# which is what ars.yml's steps do — gets `bash -e {0}`. No `--noprofile`, no
# `--norc`, and **no pipefail**; measured off run 31960152598's own step header.
# `-e` is what makes the errexit suppression inside the `&&` list observable at
# all and is present either way, so this file's obligations are unaffected —
# but supplying pipefail to a body production runs without is a FALSE GREEN in
# the direction that matters: a pipeline whose left-hand command fails is
# graded as an abort here and swallowed in CI. So the invocation is read out of
# the workflow, and a step that later gains `shell: bash` moves this harness
# with it. workflow-step.sh REFUSES rather than defaulting when it cannot find
# the step, which is why this is a hard exit and not a fallback.
#
# Derived once per step, never once per file: the three steps are independent
# declarations and any one of them could gain a `shell:` on its own.
#
# `derive_or_die` writes to a GLOBAL rather than printing its answer, because
# the obvious `x=$(derive_or_die …)` spelling runs the function in a subshell,
# where its `exit 1` exits only that subshell and the caller carries on with an
# empty invocation — a refusal that does not refuse, in a file about exactly
# that.
DERIVED=
derive_or_die() { # <step-name> -> sets DERIVED, or exits
  if ! DERIVED=$(workflow_step_shell "$WF" "$1"); then
    echo "FAIL: ars-badge-push_test — could not derive the shell $WF gives '$1' (refusal above); nothing below would have graded the real program" >&2
    exit 1
  fi
}
use_shell() { read -r -a STEP_ARGV <<<"$1"; }

derive_or_die "$STEP";         STEP_SHELL="$DERIVED"
derive_or_die "$SCAN_STEP";    SCAN_SHELL="$DERIVED"
derive_or_die "$EXTRACT_STEP"; EXTRACT_SHELL="$DERIVED"
use_shell "$STEP_SHELL"
echo "== $WF: derived step invocations =="
echo "   '$STEP' -> \`$STEP_SHELL\`"
echo "   '$SCAN_STEP' -> \`$SCAN_SHELL\`"
echo "   '$EXTRACT_STEP' -> \`$EXTRACT_SHELL\`"

# ---------------------------------------------------------------------------
# The harness: stubs, then a body, run under that shell.
#
# `git` and `sleep` are shell FUNCTIONS rather than PATH shims so the body's
# own lines survive byte-for-byte — in particular `sleep $((i * 3))`, which
# would otherwise cost 45 wall-clock seconds per exhausted-retry case.
# An unexpected git subcommand returns a loud, distinctive 99 instead of a
# quiet 0: a stub that silently answered "fine" to a call it did not model
# would make every arm below pass for a reason unrelated to its obligation.
#
# `rev-parse` answers a path that does not exist, which is this stub world's
# only honest answer: its `pull` always succeeds, so no rebase is ever left in
# progress here. `rebase` is deliberately left UNMODELLED — with rev-parse
# answering "no rebase directory" the step must never reach `git rebase
# --abort`, so a future edit that aborts unconditionally is reported by the
# loud 99 rather than passing quietly. The case that genuinely needs a rebase
# to exist is #1655's, and it runs against real git in a throwaway repo below.
stub_prelude() { # $1 = attempt the push first succeeds on (99 = never)
                 # $2 = status of `git diff --staged --quiet` (1 = changes staged)
  cat <<STUB
STUB_SUCCEED_ON=$1
STUB_STAGED=$2
STUB_PUSHES=0
git() {
  case "\$1" in
    config|add|commit) return 0 ;;
    diff)  return "\$STUB_STAGED" ;;
    pull)  return 0 ;;
    rev-parse) echo "./.stub-says-no-rebase-in-progress"; return 0 ;;
    push)
      STUB_PUSHES=\$((STUB_PUSHES + 1))
      if [ "\$STUB_PUSHES" -ge "\$STUB_SUCCEED_ON" ]; then return 0; fi
      return 1 ;;
    *) echo "STUB: unmodelled call: git \$*" >&2; return 99 ;;
  esac
}
sleep() { :; }
STUB
}

# run_body <file-with-body> <succeed-on> <staged-status> -> sets OUT / ST
run_body() {
  local body="$1" script="$TMP/step.sh"
  { stub_prelude "$2" "$3"; cat "$body"; } >"$script"
  # No `set +e` guard around this: THIS file runs under `set -uo pipefail` and
  # deliberately not `-e`, so a non-zero inner status — which is the expected
  # outcome of half the arms below — is data, not an abort. Toggling errexit
  # here would leave it ON for the rest of the file, which is the
  # option-you-cannot-see family the sibling issues (#1633, #1635) are made of.
  OUT=$("${STEP_ARGV[@]}" "$script" 2>&1)
  ST=$?
  return 0
}

# ---------------------------------------------------------------------------
# Obligation 1 — the defect, re-measured on every run.
#
# The pre-#1641 loop, verbatim, wrapped in nothing. Committed here rather than
# quoted in an issue, per AGENTS.md: "a number which documents behaviour but is
# not produced by it drifts silently".
echo "== the pre-#1641 loop shape (the defect, re-measured) =="
cat >"$TMP/predecessor.sh" <<'OLD'
git commit -m "chore: update ARS badge [skip ci]"
for i in 1 2 3 4 5; do
  git pull --rebase origin main && git push && break
  echo "Push attempt $i failed, retrying..."
  sleep $((i * 3))
done
OLD
run_body "$TMP/predecessor.sh" 99 1
if [[ "$ST" -eq 0 ]]; then
  pass "the old loop still exits 0 after five failed pushes — the hazard is real"
else
  fail "the old loop exits 0 after five failed pushes (the hazard this pins)" \
       "exit 0" "exit $ST — the hazard is GONE, so re-derive the fix rather than trusting it :: $(flat "$OUT")"
fi
want_contains "...having really exhausted all five attempts" "Push attempt 5 failed" "$OUT"

# ---------------------------------------------------------------------------
# Obligations 2-4 — the REAL step, extracted from the REAL workflow and run.
echo ""
echo "== $WF: $STEP =="

if [[ ! -f "$WF" ]]; then
  fail "$WF is readable" "the workflow file" "not found — the step check could not run"
else
  # Extract the named step's `run: |` body and dedent it — through the same
  # library that derived the invocation above, so the shell this file grades
  # under and the body it grades cannot come from two different steps. Keyed on
  # the step NAME, so a body that moved within the file is still found and a
  # step that was renamed is a loud refusal rather than a silent zero-line body.
  if ! workflow_step_body "$WF" "$STEP" >"$TMP/step-body.sh"; then
    fail "the '$STEP' step body was extracted from $WF" \
         "the step's run: | body" "workflow-step refused (above) — the scan has gone blind, not the step clean"
    : >"$TMP/step-body.sh"
  fi

  # The extraction has to have found something. A scan that silently stopped
  # matching reads exactly like a workflow with no hazard in it, and every
  # arm below would then grade an empty file — which exits 0 and looks like a
  # clean push.
  lines=$(grep -cve '^[[:space:]]*$' "$TMP/step-body.sh")
  if [[ "${lines:-0}" -lt 5 ]]; then
    fail "the '$STEP' step body was extracted from $WF" \
         "at least 5 non-blank lines" "$lines — the scan has gone blind, not the step clean"
  else
    pass "extracted the '$STEP' step body from $WF ($lines non-blank lines)"

    # Obligation 2: every attempt fails.
    run_body "$TMP/step-body.sh" 99 1
    want_status "every push attempt fails" 1 "$ST" "$OUT"
    want_contains "...and the step says the badge was NOT pushed" "NOT pushed" "$OUT"
    want_contains "...as a workflow error annotation, so it surfaces on the run" "::error::" "$OUT"
    want_contains "...having really exhausted all five attempts" "Push attempt 5 failed" "$OUT"

    # Obligation 3: the retry is still a retry. This is the arm that a "fix"
    # dropping the loop — or removing the errexit suppression inside the `&&`
    # list, which would abort at the first failed pull — cannot satisfy.
    run_body "$TMP/step-body.sh" 3 1
    want_status "a push that succeeds on the third attempt" 0 "$ST" "$OUT"
    want_contains "...retried after the first failure" "Push attempt 1 failed" "$OUT"
    want_contains "...and after the second" "Push attempt 2 failed" "$OUT"
    want_absent "...without claiming the badge went unpushed" "NOT pushed" "$OUT"

    # Obligation 4: the clean paths.
    run_body "$TMP/step-body.sh" 1 1
    want_status "a push that succeeds first time" 0 "$ST" "$OUT"
    want_absent "...with no retry noise" "Push attempt" "$OUT"

    run_body "$TMP/step-body.sh" 99 0
    want_status "nothing staged to commit" 0 "$ST" "$OUT"
    want_contains "...and it says so" "No badge changes to commit" "$OUT"

    # `|| true` is the false fix for this family and is refused by name: it
    # would let the loop end without aborting while leaving `true`'s status
    # behind, so the exhausted case would read as 0 all over again (#1629
    # measured the same thing one level down, on a `$?` capture).
    #
    # Full-line comments are stripped first, and that is not tidiness: the
    # step's own comment EXPLAINS why `|| true` is not used, so a raw scan
    # fails on the sentence saying the right thing. Measured — it did.
    code=$(grep -v '^[[:space:]]*#' "$TMP/step-body.sh")
    want_absent "the step does not reach for \`|| true\`" "|| true" "$code"
    # ...and the strip must not have eaten the code, or the arm above is
    # vacuous: an empty haystack contains no needle.
    want_contains "...checked against the step's real code, not an empty strip" "git push" "$code"
  fi
fi

# ---------------------------------------------------------------------------
# #1655 — the retry that could not retry. Obligations 12-17.
#
# The stub above cannot reach this one, and that is the point rather than a
# limitation to work around: its `git pull` returns a status and leaves no
# state, where the whole defect is STATE. A `git pull --rebase` that fails
# INSIDE the rebase — a conflict, not a rejected push — leaves the tree
# mid-rebase, and every later `git pull` then refuses outright:
#
#     error: Pulling is not possible because you have unmerged files.
#
# So four of the five attempts could not run at all, while the step's closing
# message attributed all five to the push. Measured on run 31963062408, whose
# attempt 1 conflicted `add/add` on .github/workflows/ars.yml and whose
# attempts 2-5 are verbatim identical refusals.
#
# Reachability is stated as the issue states it, and the distinction is kept:
# the MECHANISM is measured (any failed rebase leaves the same wreckage — the
# arms below reproduce it against real git), while "a push-to-main run can
# reach it" is ASSUMED. That run was a `workflow_dispatch` on a branch, so its
# ref differed from main; a badge run whose rebase conflicts on main (two runs
# racing on the same README line, a README edit landing between checkout and
# push) would burn attempts 2-5 the same way, but that has not been observed.
# `actions/checkout@v7` here carries no `fetch-depth:`, i.e. a depth-1 clone,
# which is why the rebase saw add/add rather than ordinary history — noted
# because the fixture reproduces that shape, NOT changed: the depth is a
# separate decision with its own cost.
#
# The obligations:
#
#  12. the defect is re-MEASURED against real git, not described: the pre-#1655
#      loop, run in a throwaway repo rigged to conflict, reaches a clean tree
#      on exactly ONE of its five attempts. The permanent vacuity guard for
#      13-16 — if a future git stopped leaving the tree mid-rebase, they would
#      all pass for the wrong reason.
#  13. the real step, same fixture: all five attempts start from a clean tree,
#      the loop really runs five times, and it still fails loudly — and its
#      closing message names how many attempts were MADE rather than a fixed 5.
#  14. the ordinary rejected push this loop exists for still works: rejected
#      twice, retried, accepted on the third attempt, with the commit actually
#      landing on origin. No rebase is involved anywhere in that path.
#  15. the rejected SPELLING is committed beside the shipped one and
#      re-measured: a `git rebase --abort` whose status is read but which never
#      asks whether there is a rebase to abort turns 14 red, because "no rebase
#      in progress" exits 128. It handles 12's fixture perfectly, so the
#      ordinary-rejection fixture is the only one that tells the two apart.
#  16. a first-attempt push in a real repo still lands, with no retry noise.
#  17. the refusal the fix ADDS is reached and says the right thing: an abort
#      that FAILS leaves the tree mid-rebase, so the remaining attempts are as
#      doomed as they were before the fix — the step stops, names the abort as
#      what could not be done, and reports how many attempts were made. This is
#      the one arm with no "before the fix" to be red against, so it carries an
#      injected mutation instead (see attempt_prelude).
#
# What is NOT re-measured here is the `|| true` spelling the issue names
# first. It is refused one level up, by name, by the arm above
# (`the step does not reach for \`|| true\``) — the same refusal #1629 and
# #1644 earned — so a body that discards the abort's status does not compile
# past this file, and a variant demonstrating it would need a rebase that
# cannot be aborted, which no portable fixture produces.

echo ""
echo "== $WF: $STEP — the retry that could not retry (#1655) =="

# The fixture's git runs with the developer's own configuration out of the way.
# A global `pull.rebase`, `commit.gpgsign`, `core.hooksPath` or
# `init.defaultBranch` would otherwise decide what these repos do, and these
# arms assert on the CONFLICT rather than on whoever's ~/.gitconfig ran them.
# HOME is redirected as well, for a git predating GIT_CONFIG_GLOBAL — and
# because nothing here may read or write the real one.
FIXTURE_HOME="$TMP/fixture-home"
NO_GITCONFIG="$TMP/fixture-gitconfig-that-does-not-exist"
mkdir -p "$FIXTURE_HOME" || { echo "FAIL: ars-badge-push_test — cannot create $FIXTURE_HOME" >&2; exit 1; }
fgit() {
  env HOME="$FIXTURE_HOME" GIT_CONFIG_NOSYSTEM=1 \
      GIT_CONFIG_GLOBAL="$NO_GITCONFIG" GIT_CONFIG_SYSTEM="$NO_GITCONFIG" \
      GIT_TERMINAL_PROMPT=0 git "$@"
}

# fixture_build <dir> <kind> [reject-n]
#
# A bare `origin.git` reachable only by filesystem path — nothing here fetches
# from, pushes to or otherwise contacts the real origin — plus a `work/` clone
# holding an uncommitted README.md, which is what the step's `git add README.md`
# stages.
#
#   conflict : origin/main and the badge commit each ADD README.md, so
#              `git pull --rebase origin main` conflicts add/add — the shape
#              run 31963062408 hit, reproduced here without its depth-1 clone.
#   reject   : no divergence at all. origin's pre-receive hook declines the
#              first <reject-n> pushes, which is the ordinary race the retry
#              loop exists for: the branch is not behind, `git pull --rebase`
#              is a no-op, and NOTHING is left mid-rebase.
fixture_build() {
  local dir="$1" kind="$2" rejects="${3:-0}" origin="$1/origin.git"
  rm -rf "$dir" || return 1
  mkdir -p "$dir" || return 1
  fgit init -q --bare "$origin" >/dev/null 2>&1 || return 1
  # `git init -b main` is git >= 2.28; the symbolic-ref spelling is not, and a
  # harness that refused on an older git would be reporting its own age as the
  # step's defect.
  fgit -C "$origin" symbolic-ref HEAD refs/heads/main >/dev/null 2>&1 || return 1
  fgit clone -q "$origin" "$dir/seed" >/dev/null 2>&1 || return 1
  (
    cd "$dir/seed" || exit 1
    fgit config user.name fixture || exit 1
    fgit config user.email fixture@example.invalid || exit 1
    printf 'seed\n' >seed.txt || exit 1
    fgit add seed.txt && fgit commit -qm "seed" && fgit push -q origin main
  ) >/dev/null 2>&1 || return 1

  if [[ "$kind" == conflict ]]; then
    # work/ is cloned BEFORE origin gains its README.md, so the two adds are
    # genuinely concurrent.
    fgit clone -q "$origin" "$dir/work" >/dev/null 2>&1 || return 1
    (
      cd "$dir/seed" || exit 1
      fgit pull -q --rebase origin main || exit 1
      printf 'ARS badge, as origin already has it\n' >README.md || exit 1
      fgit add README.md && fgit commit -qm "someone else's README" && fgit push -q origin main
    ) >/dev/null 2>&1 || return 1
  else
    if [[ "$rejects" -gt 0 ]]; then
      cat >"$origin/hooks/pre-receive" <<HOOK || return 1
#!/bin/sh
n=0
[ -f "\$GIT_DIR/reject-count" ] && n=\$(cat "\$GIT_DIR/reject-count")
n=\$((n + 1))
printf '%s\n' "\$n" >"\$GIT_DIR/reject-count"
if [ "\$n" -le $rejects ]; then
  echo "fixture: simulated concurrent update to main, try again" >&2
  exit 1
fi
exit 0
HOOK
      chmod +x "$origin/hooks/pre-receive" || return 1
    fi
    fgit clone -q "$origin" "$dir/work" >/dev/null 2>&1 || return 1
  fi

  (
    cd "$dir/work" || exit 1
    fgit config user.name fixture && fgit config user.email fixture@example.invalid
  ) >/dev/null 2>&1 || return 1
  printf 'ARS badge, as this run computed it\n' >"$dir/work/README.md" || return 1
  return 0
}

# The instrumentation. `git` cannot be stubbed here — the whole subject is what
# real git leaves on disk — so exactly one thing is observed: at the moment
# each attempt reaches `git pull`, was the tree mid-rebase? That is the
# property the issue is about (attempt N+1 starting from the state attempt N
# did) and it is read off git's OWN rebase state rather than off the
# "Pulling is not possible…" message, which git is free to reword.
#
# The optional second argument is the one deliberate mutation this section
# injects rather than builds: `git rebase --abort` answering non-zero. The
# refusal it drives (obligation 17) is something the fix ADDS, so it has no
# "before the fix" to be seen red against, and a rebase that genuinely cannot
# be aborted has no portable fixture — a read-only `.git` behaves differently
# per filesystem and is root-dependent. Everything else still runs against real
# git; only the abort's answer is replaced.
attempt_prelude() { # <log-file> [break-abort]
  local log="$1" brk=""
  if [[ "${2:-}" == break-abort ]]; then
    brk='  if [ "$1" = rebase ]; then echo "fixture: abort refused" >&2; return 1; fi'
  fi
  cat <<PRELUDE
git() {
  if [ "\$1" = pull ]; then
    if [ -d "\$(command git rev-parse --git-path rebase-merge 2>/dev/null)" ] ||
       [ -d "\$(command git rev-parse --git-path rebase-apply 2>/dev/null)" ]; then
      echo mid-rebase >>"$log"
    else
      echo clean >>"$log"
    fi
  fi
$brk
  command git "\$@"
}
sleep() { :; }
PRELUDE
}

# run_in_repo <fixture-dir> <body-file> [break-abort]
#   -> sets OUT / ST / PULLS / CLEAN_STARTS
run_in_repo() {
  local dir="$1" body="$2" log="$1/.attempts" script="$1/.step.sh"
  : >"$log"
  { attempt_prelude "$log" "${3:-}"; cat "$body"; } >"$script"
  use_shell "$STEP_SHELL"
  # Same reasoning as run_body: no `set +e` toggle, a non-zero inner status is
  # data. The counts are read with awk rather than `grep -c`, which exits 1 on
  # no match and would need an `|| …` that can append a second line to the
  # capture.
  OUT=$(cd "$dir/work" && env HOME="$FIXTURE_HOME" GIT_CONFIG_NOSYSTEM=1 \
        GIT_CONFIG_GLOBAL="$NO_GITCONFIG" GIT_CONFIG_SYSTEM="$NO_GITCONFIG" \
        GIT_TERMINAL_PROMPT=0 "${STEP_ARGV[@]}" "$script" 2>&1)
  ST=$?
  PULLS=$(awk 'END{print NR+0}' "$log")
  CLEAN_STARTS=$(awk '$0=="clean"{n++} END{print n+0}' "$log")
  return 0
}

want_starts() { # label want-clean want-pulls
  if [[ "$CLEAN_STARTS" == "$2" && "$PULLS" == "$3" ]]; then
    pass "$1 ($CLEAN_STARTS of $PULLS attempts started from a clean tree)"
  else
    fail "$1" "$2 clean start(s) out of $3 pulls" \
         "$CLEAN_STARTS of $PULLS :: $(flat "$OUT")"
  fi
  return 0
}

# The committed predecessor: today's loop, verbatim, with no abort anywhere.
cat >"$TMP/pre1655.sh" <<'OLD'
git config user.name "github-actions[bot]"
git config user.email "github-actions[bot]@users.noreply.github.com"
git add README.md
git commit -m "chore: update ARS badge [skip ci]"
pushed=0
for i in 1 2 3 4 5; do
  if git pull --rebase origin main && git push; then pushed=1; break; fi
  echo "Push attempt $i failed, retrying..."
  sleep $((i * 3))
done
if [ "$pushed" -ne 1 ]; then
  echo "::error::ARS badge was NOT pushed: all 5 attempts failed."
  exit 1
fi
OLD

# The rejected SPELLING, committed beside the one that shipped rather than
# described in a PR body: a `git rebase --abort` whose status IS read, placed
# after each failed attempt, which never asks whether there is a rebase to
# abort. It is the reading of the issue's second suggestion a careful person
# actually writes, and `git rebase --abort` with no rebase in progress exits
# 128 — so every healthy retry becomes a reported failure.
cat >"$TMP/naive1655.sh" <<'NAIVE'
git config user.name "github-actions[bot]"
git config user.email "github-actions[bot]@users.noreply.github.com"
git add README.md
git commit -m "chore: update ARS badge [skip ci]"
pushed=0
for i in 1 2 3 4 5; do
  if git pull --rebase origin main && git push; then pushed=1; break; fi
  echo "Push attempt $i failed, retrying..."
  if ! git rebase --abort; then
    echo "::error::naive spelling: attempt $i's rebase could not be aborted."
    exit 1
  fi
  sleep $((i * 3))
done
if [ "$pushed" -ne 1 ]; then
  echo "::error::ARS badge was NOT pushed: all 5 attempts failed."
  exit 1
fi
NAIVE

# The refusal. If the fixture does not actually conflict, every arm below
# grades a program that never met the defect — a run that "found nothing" and
# a run that could not look must not print the same thing. Probed on its OWN
# fixture instance, because probing consumes the tree it leaves mid-rebase.
fixture_conflicts() { # <dir>
  (
    cd "$1/work" || exit 1
    fgit add README.md >/dev/null 2>&1 || exit 1
    fgit commit -qm "chore: update ARS badge [skip ci]" >/dev/null 2>&1 || exit 1
    fgit pull --rebase origin main >/dev/null 2>&1 && exit 1
    [ -d "$(fgit rev-parse --git-path rebase-merge)" ] ||
      [ -d "$(fgit rev-parse --git-path rebase-apply)" ]
  )
}

if [[ ! -s "$TMP/step-body.sh" ]]; then
  fail "the '$STEP' step body is available for the #1655 arms" \
       "the extracted body" "empty or missing — the extraction above refused, so these arms would grade nothing"
elif ! fixture_build "$TMP/fix-probe" conflict; then
  fail "the #1655 conflict fixture could be built" \
       "a throwaway origin.git + work clone under $TMP" \
       "fixture_build failed — these arms cannot run, and a skip here would read as a pass"
elif ! fixture_conflicts "$TMP/fix-probe"; then
  fail "the #1655 conflict fixture produces a rebase that really conflicts" \
       "\`git pull --rebase origin main\` failing and leaving a rebase in progress" \
       "it did not — this git no longer reproduces the defect's precondition, so nothing below would be graded"
else
  pass "the #1655 conflict fixture leaves a real rebase in progress ($(fgit --version))"

  # Obligation 12 — the defect, re-measured against real git.
  if ! fixture_build "$TMP/fix-old" conflict; then
    fail "a fresh conflict fixture for the pre-#1655 loop" "a built fixture" "fixture_build failed"
  else
    run_in_repo "$TMP/fix-old" "$TMP/pre1655.sh"
    want_starts "the pre-#1655 loop reaches a clean tree on exactly 1 of its 5 attempts — the hazard is real" 1 5
    want_status "...and still fails, for a cause four attempts never tested" 1 "$ST" "$OUT"
  fi

  # Obligation 13 — the real step, same fixture.
  if ! fixture_build "$TMP/fix-new" conflict; then
    fail "a fresh conflict fixture for the real step" "a built fixture" "fixture_build failed"
  else
    run_in_repo "$TMP/fix-new" "$TMP/step-body.sh"
    want_starts "every one of the 5 attempts starts from a clean tree" 5 5
    want_status "...and a rebase that keeps conflicting still fails the step" 1 "$ST" "$OUT"
    want_contains "...as a workflow error annotation" "::error::" "$OUT"
    want_contains "...saying the badge was NOT pushed" "NOT pushed" "$OUT"
    want_contains "...having really reached the fifth attempt" "Push attempt 5 failed" "$OUT"
    want_contains "...and reporting that it undid each conflicted rebase" "unfinished rebase was in progress" "$OUT"
    # The issue's closing line: "all 5 attempts failed" is only true if all 5
    # were attempted. The count is DERIVED from the loop rather than typed, so
    # the sentence cannot outrun the facts; the needle here is the measurement
    # this run just took, not a literal.
    want_contains "...naming the number of attempts actually made, not a fixed 5" "$PULLS of 5" "$OUT"
    origin_head=$(fgit -C "$TMP/fix-new/origin.git" log --oneline -1 2>&1)
    want_absent "...and origin/main really did not get the badge commit" "update ARS badge" "$origin_head"
  fi

  # Obligation 14 — the ordinary rejected push the loop exists for. THE arm a
  # status-reading abort with no in-progress check turns red (obligation 15).
  if ! fixture_build "$TMP/fix-race" reject 2; then
    fail "a rejected-push fixture for the real step" "a built fixture" "fixture_build failed"
  else
    run_in_repo "$TMP/fix-race" "$TMP/step-body.sh"
    want_status "a push rejected twice, no rebase anywhere, succeeds on the third attempt" 0 "$ST" "$OUT"
    want_starts "...all three attempts having started from a clean tree" 3 3
    want_contains "...retried after the first rejection" "Push attempt 1 failed" "$OUT"
    want_contains "...and after the second" "Push attempt 2 failed" "$OUT"
    want_absent "...with no error annotation" "::error::" "$OUT"
    want_absent "...and no claim that the badge went unpushed" "NOT pushed" "$OUT"
    origin_head=$(fgit -C "$TMP/fix-race/origin.git" log --oneline -1 2>&1)
    want_contains "...the badge commit having really landed on origin/main" "update ARS badge" "$origin_head"
  fi

  # Obligation 15 — the rejected spelling, committed and re-measured.
  if ! fixture_build "$TMP/fix-naive-race" reject 2; then
    fail "a rejected-push fixture for the naive spelling" "a built fixture" "fixture_build failed"
  else
    run_in_repo "$TMP/fix-naive-race" "$TMP/naive1655.sh"
    if [[ "$ST" -ne 0 ]]; then
      pass "an unguarded \`git rebase --abort\` breaks the ordinary rejected push (exit $ST) — why the step asks first"
    else
      fail "an unguarded \`git rebase --abort\` breaks the ordinary rejected push (the reason the shipped spelling asks whether a rebase is in progress)" \
           "a non-zero exit" "exit 0 — that spelling is no longer distinguishable from the shipped one, so re-derive the choice :: $(flat "$OUT")"
    fi
    want_contains "...giving up on the very first retry, on an abort that had nothing to abort" \
                  "naive spelling: attempt 1's rebase could not be aborted" "$OUT"
  fi
  if ! fixture_build "$TMP/fix-naive-conflict" conflict; then
    fail "a conflict fixture for the naive spelling" "a built fixture" "fixture_build failed"
  else
    # ...and it handles the CONFLICT fixture perfectly. Without this, the
    # obligation above reads as "that spelling is broken", when what is true is
    # narrower and is the whole reason 14 exists: the two spellings are
    # indistinguishable on the fixture the issue was reported from.
    run_in_repo "$TMP/fix-naive-conflict" "$TMP/naive1655.sh"
    want_starts "the naive spelling is indistinguishable from the shipped one on a CONFLICTING fixture" 5 5
  fi

  # Obligation 17 — the refusal the fix ADDS, and its only coverage. An abort
  # that fails is the one case where asking first is not enough: the tree is
  # still mid-rebase, so the remaining attempts are exactly as doomed as they
  # were before the fix. The step must stop and say so rather than burn them.
  if ! fixture_build "$TMP/fix-stuck" conflict; then
    fail "a conflict fixture for the unabortable rebase" "a built fixture" "fixture_build failed"
  else
    run_in_repo "$TMP/fix-stuck" "$TMP/step-body.sh" break-abort
    want_status "a rebase that cannot be aborted fails the step" 1 "$ST" "$OUT"
    want_contains "...as a workflow error annotation" "::error::" "$OUT"
    want_contains "...naming the abort as what could not be done" "could not undo it" "$OUT"
    want_contains "...and saying how many attempts were made, not claiming 5" "Only 1 of 5" "$OUT"
    want_starts "...having stopped instead of burning the remaining attempts" 1 1
  fi

  # Obligation 16 — the clean path, in a real repo.
  if ! fixture_build "$TMP/fix-clean" reject 0; then
    fail "a clean fixture for the real step" "a built fixture" "fixture_build failed"
  else
    run_in_repo "$TMP/fix-clean" "$TMP/step-body.sh"
    want_status "a first-attempt push in a real repo" 0 "$ST" "$OUT"
    want_starts "...having pulled exactly once" 1 1
    want_absent "...with no retry noise" "Push attempt" "$OUT"
    origin_head=$(fgit -C "$TMP/fix-clean/origin.git" log --oneline -1 2>&1)
    want_contains "...and the badge commit on origin/main" "update ARS badge" "$origin_head"
  fi
fi

# ---------------------------------------------------------------------------
# #1644 — the two steps that PRODUCE the badge "Commit badge update" pushes.
#
# Every case runs in a throwaway directory holding exactly what the runner's
# workspace holds at that point (an `ars-output.txt`, a `README.md`), so the
# repo's real README.md is never written to. The one arm that grades the real
# README COPIES it in — that arm is the reason `sed`'s pattern is checked
# against the file it actually has to match rather than against a fixture
# written to match it.

want_file_contains() { # label needle file
  if grep -qF "$2" "$3" 2>/dev/null; then pass "$1"
  else fail "$1" "$3 containing: $2" "$(flat "$(cat "$3" 2>/dev/null)")"; fi
  return 0
}
want_file_absent() { # label needle file
  if grep -qF "$2" "$3" 2>/dev/null; then fail "$1" "$3 NOT containing: $2" "$(flat "$(cat "$3" 2>/dev/null)")"
  else pass "$1"; fi
  return 0
}

# The `ars` stub prints ./.ars-stdout and returns the status it was built with.
# A subcommand it does not model answers a loud, distinctive 99 naming the call,
# and a MISSING ./.ars-stdout a 98 — never a quiet 0, which is what would make
# every arm below pass for a reason unrelated to its obligation.
ars_stub() { # $1 = status the stub returns for `ars scan`
  cat <<STUB
ars() {
  case "\$1" in
    scan) cat ./.ars-stdout || { echo "STUB: no ./.ars-stdout in \$(pwd)" >&2; return 98; }
          return $1 ;;
    *)    echo "STUB: unmodelled call: ars \$*" >&2; return 99 ;;
  esac
}
STUB
}

# GNU vs BSD `sed -i`. ars.yml runs on ubuntu-latest, where `-i` takes no
# argument; this harness runs wherever the shell-lib suite runs, which includes
# test.yml's macos-latest runner and every developer's Mac, where BSD sed reads
# the SCRIPT as `-i`'s backup suffix. Measured on this machine:
# `sed -i "s|a|b|g" f` -> `sed: 1: "…": invalid command code f`, file unchanged.
# So `-i` is re-expressed portably over the REAL sed, preserving the script
# semantics this file grades — in particular that a script matching nothing
# rewrites the file with identical bytes and exits 0, which is obligation 11's
# whole subject. Any other `-i` form is a loud refusal, not a quiet 0.
sed_shim() {
  cat <<'STUB'
sed() {
  if [ "$1" = "-i" ]; then
    if [ "$#" -ne 3 ]; then echo "STUB: unmodelled sed -i form: sed $*" >&2; return 99; fi
    command sed "$2" "$3" >"$3.stub.new" || return $?
    command mv "$3.stub.new" "$3" || return $?
    return 0
  fi
  command sed "$@"
}
STUB
}

CASE=
new_case() { # <name> -> sets CASE to a fresh, empty directory
  CASE="$TMP/case-$1"
  rm -rf "$CASE"
  mkdir -p "$CASE" || { echo "FAIL: ars-badge-push_test — cannot create case dir $CASE" >&2; exit 1; }
  { ars_stub "${2:-0}"; sed_shim; } >"$CASE/.prelude"
}

run_step() { # <workdir> <shell-string> <body-file> -> sets OUT / ST
  local dir="$1" body="$3" script="$1/.step.sh"
  use_shell "$2"
  { cat "$dir/.prelude"; cat "$body"; } >"$script"
  # Same reasoning as run_body: no `set +e` toggle, because a non-zero inner
  # status is the EXPECTED outcome of most arms here and is data, not an abort.
  OUT=$(cd "$dir" && "${STEP_ARGV[@]}" .step.sh 2>&1)
  ST=$?
  return 0
}

extract_body() { # <step-name> <min-non-blank-lines> <outfile> -> 0 on success
  local name="$1" min="$2" out="$3" lines
  if ! workflow_step_body "$WF" "$name" >"$out"; then
    fail "the '$name' step body was extracted from $WF" \
         "the step's run: | body" "workflow-step refused (above) — the scan has gone blind, not the step clean"
    return 1
  fi
  lines=$(grep -cve '^[[:space:]]*$' "$out")
  if [[ "${lines:-0}" -lt "$min" ]]; then
    fail "the '$name' step body was extracted from $WF" \
         "at least $min non-blank lines" "$lines — the scan has gone blind, not the step clean"
    return 1
  fi
  pass "extracted the '$name' step body from $WF ($lines non-blank lines)"
  return 0
}

# The four refusals must be told apart by their WORDING, not only by a shared
# non-zero. Three refusals printing the same sentence would satisfy every
# status arm below and leave an operator exactly where the silent `if` skips
# left them: something is red, and no idea which thing broke.
P_SCANFAIL='ARS scan FAILED'
P_NOBADGE='carries no img.shields.io/badge/ARS line'
P_NOURL='could be read out of it'
P_NOWRITE='does not contain the badge URL this step just wrote'

SCAN_ERR='ars: command failed: no such subcommand'
NEW_URL='https://img.shields.io/badge/ARS-Agent--Ready%209.9%2F10-brightgreen'
badge_output() { # <url> -> a plausible `ars scan --badge` tail
  printf 'Badge\n----------------------------------------\n[![ARS](%s)](https://github.com/ingo-eichhorst/agent-readyness)\n' "$1"
}

# The real README is read, not assumed: if it carries no ARS badge URL there is
# nothing for this workflow to rewrite, and obligations 8 and 5 would both be
# grading a fixture written to match the pattern under test.
OLD_URL=$(grep -o 'https://img\.shields\.io/badge/ARS[^)]*' README.md | head -1 || echo "")

echo ""
echo "== $WF: $SCAN_STEP / $EXTRACT_STEP =="

if [[ -z "$OLD_URL" ]]; then
  fail "README.md carries an ARS badge URL for the badge job to rewrite" \
       "one https://img.shields.io/badge/ARS-… URL in README.md" \
       "none — either the badge was removed (this job then has nothing to do and should say so) or this scan has gone blind"
elif [[ "$OLD_URL" == "$NEW_URL" ]]; then
  fail "the fixture score differs from README's, or the rewrite arm is vacuous" \
       "a NEW_URL unlike README's current badge" "both are $OLD_URL"
else
  pass "read README.md's current badge URL ($OLD_URL)"

  # -------------------------------------------------------------------------
  # Obligation 5 — the pre-#1644 bodies, verbatim, re-measured on every run.
  #
  # Committed rather than quoted in the issue, per AGENTS.md: "a number which
  # documents behaviour but is not produced by it drifts silently". This is
  # also the vacuity guard for 6-11: if `|| true` + a trailing `cat` ever
  # stopped exiting 0, the fix would be protecting nothing.
  cat >"$TMP/pre1644-scan.sh" <<'OLD'
ars scan ./core --no-llm --badge > ars-output.txt 2>&1 || true
cat ars-output.txt
OLD
  cat >"$TMP/pre1644-extract.sh" <<'OLD'
ARS_BADGE=$(grep "img.shields.io/badge/ARS" ars-output.txt | head -1 || echo "")
echo "ARS Badge line: $ARS_BADGE"

if [ -n "$ARS_BADGE" ]; then
  ARS_URL=$(echo "$ARS_BADGE" | grep -o 'https://img\.shields\.io/badge/ARS[^)]*' | head -1 || echo "")
  echo "ARS URL: $ARS_URL"
  if [ -n "$ARS_URL" ]; then
    sed -i "s|https://img.shields.io/badge/ARS-[^)]*|${ARS_URL}|g" README.md
    echo "README updated with ARS badge URL"
  fi
fi
OLD

  echo ""
  echo "-- the pre-#1644 bodies (the defect, re-measured) --"
  new_case pre1644 1
  printf '%s\n' "$SCAN_ERR" >"$CASE/.ars-stdout"
  cp README.md "$CASE/README.md"
  run_step "$CASE" "$SCAN_SHELL" "$TMP/pre1644-scan.sh"
  if [[ "$ST" -eq 0 ]]; then
    pass "the old scan body still exits 0 with a failing \`ars\` — the hazard is real"
  else
    fail "the old scan body exits 0 with a failing \`ars\` (the hazard this pins)" \
         "exit 0" "exit $ST — the hazard is GONE, so re-derive the fix rather than trusting it :: $(flat "$OUT")"
  fi
  run_step "$CASE" "$EXTRACT_SHELL" "$TMP/pre1644-extract.sh"
  want_status "...and the old extract body over that error output" 0 "$ST" "$OUT"
  want_file_contains "...leaving README.md's badge exactly as it was" "$OLD_URL" "$CASE/README.md"

  # -------------------------------------------------------------------------
  # Obligations 6-7 — the REAL "Run ARS scan" step, extracted and executed.
  echo ""
  echo "-- $SCAN_STEP --"
  # A floor of 2, not of "however many lines the fixed step has": the pre-#1644
  # body was two lines, so a higher floor would report the mutation that
  # RESTORES it as "the scan has gone blind" rather than as obligation 6 going
  # red — a negative result for the wrong reason reads as coverage it is not.
  # `workflow_step_body` already refuses a zero-line body, which is the blindness
  # this floor exists on top of.
  if extract_body "$SCAN_STEP" 2 "$TMP/scan-body.sh"; then
    new_case scanfail 3
    printf '%s\n' "$SCAN_ERR" >"$CASE/.ars-stdout"
    run_step "$CASE" "$SCAN_SHELL" "$TMP/scan-body.sh"
    if [[ "$ST" -eq 0 ]]; then
      fail "a failing \`ars scan\` fails the step" "a non-zero exit" "exit 0 :: $(flat "$OUT")"
    else
      pass "a failing \`ars scan\` fails the step (exit $ST)"
    fi
    want_contains "...as a workflow error annotation, so it surfaces on the run" "::error::" "$OUT"
    want_contains "...naming the scan as what failed" "$P_SCANFAIL" "$OUT"
    want_contains "...and that the badge was therefore not updated" "NOT updated" "$OUT"
    # Without this the failure has no diagnosis anywhere: `ars`'s own message is
    # redirected into the file, so a step that exits 1 without printing it is a
    # red X over an empty log.
    want_contains "...with the scan's own output still in the log" "$SCAN_ERR" "$OUT"

    # Obligation 7, the vacuity guard: a step that failed unconditionally would
    # satisfy every arm above.
    new_case scanok 0
    badge_output "$NEW_URL" >"$CASE/.ars-stdout"
    run_step "$CASE" "$SCAN_SHELL" "$TMP/scan-body.sh"
    want_status "a succeeding \`ars scan\` still passes" 0 "$ST" "$OUT"
    want_absent "...with no error annotation" "::error::" "$OUT"
    want_file_contains "...having captured the scan output for the next step" "$NEW_URL" "$CASE/ars-output.txt"

    # `|| true` is the false fix for this family and is refused by name — it is
    # what the step used to carry. Comments are stripped first for the same
    # reason the push arm strips them: the workflow's prose explains why it is
    # not used, and a raw scan fails on the sentence saying the right thing.
    code=$(grep -v '^[[:space:]]*#' "$TMP/scan-body.sh")
    want_absent "the scan step does not reach for \`|| true\`" "|| true" "$code"
    want_contains "...checked against the step's real code, not an empty strip" "ars scan" "$code"
  fi

  # -------------------------------------------------------------------------
  # Obligations 8-11 — the REAL "Extract and update ARS badge" step.
  echo ""
  echo "-- $EXTRACT_STEP --"
  if extract_body "$EXTRACT_STEP" 8 "$TMP/extract-body.sh"; then
    # Obligation 8, the vacuity guard for 9-11: a fix that refuses everything
    # is indistinguishable from one that works until something still works.
    # Graded against the REAL README.md, so the `sed` pattern is checked
    # against the file it has to match.
    new_case extractok 0
    badge_output "$NEW_URL" >"$CASE/ars-output.txt"
    cp README.md "$CASE/README.md"
    run_step "$CASE" "$EXTRACT_SHELL" "$TMP/extract-body.sh"
    want_status "a normal run still updates the badge" 0 "$ST" "$OUT"
    want_contains "...and says so" "README updated with ARS badge URL" "$OUT"
    want_file_contains "...having written the new URL into README.md" "$NEW_URL" "$CASE/README.md"
    want_file_absent "...and replaced the old one rather than appending" "$OLD_URL" "$CASE/README.md"
    want_absent "...with no error annotation" "::error::" "$OUT"

    # Obligation 9 — output with no badge line at all.
    new_case nobadge 0
    printf '%s\n' "$SCAN_ERR" >"$CASE/ars-output.txt"
    cp README.md "$CASE/README.md"
    run_step "$CASE" "$EXTRACT_SHELL" "$TMP/extract-body.sh"
    if [[ "$ST" -eq 0 ]]; then
      fail "output with no badge line fails the step" "a non-zero exit" "exit 0 :: $(flat "$OUT")"
    else
      pass "output with no badge line fails the step (exit $ST)"
    fi
    want_contains "...as a workflow error annotation" "::error::" "$OUT"
    want_contains "...naming the missing badge line specifically" "$P_NOBADGE" "$OUT"
    want_contains "...and printing the output it could not read a badge out of" "$SCAN_ERR" "$OUT"
    want_absent "...not confused with the unreadable-URL refusal" "$P_NOURL" "$OUT"
    want_absent "...nor with the rewrite-did-not-land refusal" "$P_NOWRITE" "$OUT"
    want_file_contains "...leaving README.md's badge untouched" "$OLD_URL" "$CASE/README.md"

    # Obligation 10 — a badge line whose URL cannot be read. Same shape one
    # level down, and the issue asked for it by name.
    new_case nourl 0
    printf '[![ARS](img.shields.io/badge/ARS-unparseable)](x)\n' >"$CASE/ars-output.txt"
    cp README.md "$CASE/README.md"
    run_step "$CASE" "$EXTRACT_SHELL" "$TMP/extract-body.sh"
    if [[ "$ST" -eq 0 ]]; then
      fail "a badge line with no extractable URL fails the step" "a non-zero exit" "exit 0 :: $(flat "$OUT")"
    else
      pass "a badge line with no extractable URL fails the step (exit $ST)"
    fi
    want_contains "...naming the unreadable URL specifically" "$P_NOURL" "$OUT"
    want_absent "...not confused with the missing-badge-line refusal" "$P_NOBADGE" "$OUT"
    want_absent "...nor with the rewrite-did-not-land refusal" "$P_NOWRITE" "$OUT"
    want_file_contains "...leaving README.md's badge untouched" "$OLD_URL" "$CASE/README.md"

    # Obligation 11 — the rewrite that matched nothing. `sed` exits 0 having
    # matched nothing, so before #1644 this printed "README updated" over an
    # unchanged file and the push step then reported `No badge changes`.
    new_case nowrite 0
    badge_output "$NEW_URL" >"$CASE/ars-output.txt"
    printf '# Irrlicht\n\nA README carrying no ARS badge at all.\n' >"$CASE/README.md"
    run_step "$CASE" "$EXTRACT_SHELL" "$TMP/extract-body.sh"
    if [[ "$ST" -eq 0 ]]; then
      fail "a rewrite that matched nothing fails the step" "a non-zero exit" "exit 0 :: $(flat "$OUT")"
    else
      pass "a rewrite that matched nothing fails the step (exit $ST)"
    fi
    want_contains "...naming the unwritten README specifically" "$P_NOWRITE" "$OUT"
    want_absent "...and does NOT claim the README was updated" "README updated with ARS badge URL" "$OUT"
    want_absent "...not confused with the missing-badge-line refusal" "$P_NOBADGE" "$OUT"
    want_absent "...nor with the unreadable-URL refusal" "$P_NOURL" "$OUT"

    # The step's two `|| echo ""` are deliberate and stay — they keep an empty
    # capture readable so the named refusal above can report it, rather than
    # letting errexit abort with a line number. `|| true` is the different
    # thing, and the one this family keeps having to refuse.
    code=$(grep -v '^[[:space:]]*#' "$TMP/extract-body.sh")
    want_absent "the extract step does not reach for \`|| true\`" "|| true" "$code"
    want_contains "...checked against the step's real code, not an empty strip" "sed -i" "$code"
  fi
fi

# ---------------------------------------------------------------------------
# ...and preflight's `tools` gate has to FIRE on a diff touching only ars.yml,
# or under --changed (the pre-push hook's path) every assertion above is
# skipped on precisely the commit that can break it. That is #1591's, #1629's
# and #1639's shape, each fixed by widening this same trigger. The regex is
# EXTRACTED and matched rather than string-compared, so this is a behavioural
# assertion and not a lock on one spelling of an alternation.
echo ""
echo "== tools/preflight.sh's \`tools\` trigger =="
PF=tools/preflight.sh
if [[ ! -f "$PF" ]]; then
  fail "$PF is readable" "the preflight script" "not found — the trigger check could not run"
else
  tools_re=$(grep -a "run_gate_scoped '\^tools/lib/" "$PF" \
             | sed -E "s/^[[:space:]]*run_gate_scoped '//; s/'[[:space:]]*\\\\?[[:space:]]*$//")
  if [[ -z "$tools_re" ]]; then
    fail "the tools-gate trigger regex could be read from $PF" \
         "one run_gate_scoped line starting ^tools/lib/" "no such line — the scan has gone blind, not the trigger wrong"
  else
    pass "read the tools-gate trigger regex from $PF"
    for probe in "$WF" tools/lib/ars-badge-push_test.sh; do
      if printf '%s\n' "$probe" | grep -qE "$tools_re"; then
        pass "...it fires on a diff touching $probe"
      else
        fail "the tools gate fires on a diff touching $probe" "a match" "no match against: $tools_re"
      fi
    done
    # The vacuity guard: a trigger that matched everything would satisfy both
    # probes above and scope nothing.
    if printf '%s\n' core/domain/session.go | grep -qE "$tools_re"; then
      fail "the tools-gate trigger still scopes" "no match for core/domain/session.go" "it matches everything: $tools_re"
    else
      pass "...and still does not fire on an unrelated core/ file"
    fi
  fi
fi

echo ""
if [[ "$rc" -eq 0 ]]; then
  echo "ars-badge-push_test: ALL PASS"
else
  echo "ars-badge-push_test: FAILURES" >&2
fi
exit "$rc"
