#!/usr/bin/env bash
# ars-badge-push_test.sh — the lock on .github/workflows/ars.yml's badge job.
#
# The filename no longer describes the file at all, and that is deliberate.
# It was written for a "Commit badge update" step (#1641), grew to cover
# "Run ARS scan" and "Extract and update ARS badge" (#1644), grew again for
# the same push step's rebase wreckage (#1655) — and then #1654 DELETED the
# push. There is no longer a push anywhere in this workflow to be the "push"
# in this filename: the badge goes to a gist, README is never rewritten, and
# the job holds `contents: read`. Renaming the file was declined as churn
# against six commits of history; this header is the description instead.
#
# One harness per workflow rather than one per step, because the steps share a
# workflow file, a `workflow_step_shell` derivation, an assertion vocabulary
# and one preflight trigger; splitting them would duplicate all four and let
# the copies disagree, which is what tools/lib/workflow-step.sh exists to stop.
#
# ---------------------------------------------------------------------------
# What #1654 removed, and why those obligations are GONE rather than adapted
#
# AGENTS.md: "a rewritten guard replays its predecessor's cases or it is not
# known to be a superset." That rule is about a guard being REWRITTEN. Here a
# mechanism was REMOVED — `.github/workflows/ars.yml` no longer commits, no
# longer pushes, and no longer touches README.md — so the obligations that
# graded the push step have nothing left to grade. Contorting them into
# passing against a step that does not exist would be the exact failure this
# family is about: a green that asserts nothing. They are deleted, and named
# here so the deletion is on the record rather than discovered by a reviewer:
#
#   #1641, obligations 1-4 — the pre-fix retry loop replayed verbatim (five
#     failed pushes exiting 0); the real step failing loudly when every
#     attempt failed; the third-attempt arm proving the retry is still a
#     retry; the clean paths (nothing staged, first-attempt push).
#   #1655, obligations 12-17 — the same loop against REAL git in a throwaway
#     repo: the pre-fix loop reaching a clean tree on 1 of 5 attempts; every
#     attempt starting clean after the fix; the ordinary rejected push
#     succeeding on the third try; the rejected `git rebase --abort` spelling
#     committed beside the shipped one; a first-attempt push landing on
#     origin; and the refusal for a rebase that cannot be aborted.
#
# With them went their machinery: the `git`/`sleep` stub, the real-git fixture
# builder (`fixture_build`, `fgit`, `fixture_conflicts`), the mid-rebase
# instrumentation (`attempt_prelude`, `run_in_repo`, `want_starts`), and the
# two committed predecessor loops. `tools/lib/workflow-step_test.sh` moved its
# real-workflow row and its `shell:` mutation target from 'Commit badge update'
# to 'Run ARS scan' for the same reason.
#
# What that deletion COSTS is worth stating plainly: the defect #1641 and
# #1655 fixed — a retry loop whose exhausted case reads as success — is no
# longer covered anywhere, because no retry loop remains in this repo's
# workflows. If one is ever written again, those obligations are in git
# history at ae85182f and should be brought back with it.
#
# ---------------------------------------------------------------------------
# What #1654 was, and what this file now grades
#
# The badge job pushed its commit straight to `main`, which the "Protect Main"
# ruleset refuses with `GH013: Repository rule violations found`. Not a
# transient condition, so no retry could ever clear it: every run since
# 2026-04-26 computed a score, rewrote README.md on the runner and threw it
# away. README read `8.1/10` while the scan returned `7.9/10` — four months
# wrong, and green about it until #1647 made the failure loud.
#
# The fix is codescene-badge.yml's shape: build a shields.io ENDPOINT payload
# and PATCH it into the gist README already reads its Coverage and CodeScene
# badges from. Nothing is committed, so nothing can be refused.
#
# The obligations, in order. A-group is #1644's, unchanged in kind; B-group is
# #1644's extract obligations adapted to a step that builds a payload instead
# of rewriting a file, plus the two refusals the decode ADDS; C-group is all
# new and therefore carries AGENTS.md's mutation rule in its strongest form —
# every one of them passes the moment it is written, so each was seen red by a
# deliberate mutation before landing (the mutations are named in the PR body
# and reproducible from the arms below).
#
#   A1. the pre-#1644 scan body is re-MEASURED, not described: emitted verbatim
#       and run against a failing `ars`, it still exits 0. The permanent
#       vacuity guard for A2-A3 — if `|| true` + a trailing `cat` ever stopped
#       exiting 0, the fix would be protecting nothing.
#   A2. a FAILED scan is no longer green, and the step says the badge was not
#       updated. The scan's own output is still printed, or the failure has no
#       diagnosis in the log.
#   A3. a scan that SUCCEEDS is still green (the vacuity guard for A2 — a step
#       that failed unconditionally would satisfy A2 perfectly).
#   B1. the pre-#1644 extract body, verbatim, over an error output: still exit
#       0, still silent. The half of that predecessor that rewrote README is
#       never reached on this path, which is the point — the hazard was the
#       silent SKIP, and the skip is what the new refusals replace.
#   B2. a normal run still produces a badge: a valid shields.io endpoint
#       payload with the scanned score in it. The vacuity guard for B3-B6, and
#       the only arm that would notice the whole step being replaced by
#       `exit 1`.
#   B3. output with NO badge line is a named failure.
#   B4. a badge line carrying no extractable URL is its OWN named failure.
#   B5. a URL that does not split into label/message/color is its OWN named
#       failure — the "empty badge value" case, which before #1654 could not
#       arise because the URL was copied around as one opaque string.
#   B6. a URL carrying a percent-escape the decode does not model is its OWN
#       named failure, rather than a badge that renders `%3A` at the reader.
#   C1. the gist step publishes: the endpoint payload is PATCHed to
#       api.github.com/gists/<id> with the token, and the step is green.
#   C2. an unset GIST_SECRET is a named failure, and nothing is sent.
#   C3. an unset COVERAGE_GIST_ID is a named failure, and nothing is sent.
#   C4. a missing or empty-message payload is a named failure, and nothing is
#       sent — "the extract step produced nothing" and "the API refused" must
#       not read alike.
#   C5. a non-2xx answer is a named failure, with the API's own body in the
#       log.
#   C6. a curl that never reached the API is the same named failure and says
#       so — under this step's implicit `-e` a bare `$(curl …)` would abort
#       with no annotation at all, which is the "could not be attempted"
#       outcome going silent.
#   C7. the job writes NOTHING to the repository: no `contents: write`, no
#       `git push`. That is a lock (it passes by construction), so it is
#       driven against deliberately mutated copies as well as the real file.
#
# EVERY refusal above additionally asserts that no OTHER refusal's wording is
# present. Refusals that all fire together, or that print the same sentence,
# satisfy every status arm while leaving the operator exactly where the silent
# `if` skips left them: something is red, and no idea which thing broke
# (#1677/#1678).
#
# Deliberately NOT a general workflow linter, and the measurement is the
# honest part. Over this repo's multi-line `run:` blocks, a rule keyed on "the
# block's last statement is echo/sleep/cat/printf" flags 6 blocks of which 4
# are correct code; a rule keyed on `|| true` flags 2. No candidate rule both
# catches the subject and stays quiet on the correct blocks, so a linter's
# green would claim coverage it does not have. This is a lock on the ONE
# workflow that exists — the same conclusion, reached the same way, as #1629's
# scan in swift-suite_test.sh.
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
# jq is not stubbed: the payload the gist step builds has to be REAL JSON, and
# only jq can say so. `curl` deliberately is NOT needed — it is shadowed by a
# shell function in every arm below, so nothing here can reach the network.
need jq

WF=.github/workflows/ars.yml
SCAN_STEP='Run ARS scan'
EXTRACT_STEP='Extract ARS badge'
GIST_STEP='Update Gist with ARS score'

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
want_nonzero() { # label got out
  if [[ "$2" -ne 0 ]]; then pass "$1 (exit $2)"; else fail "$1" "a non-zero exit" "exit 0 :: $(flat "$3")"; fi
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
want_file_contains() { # label needle file
  if grep -qF "$2" "$3" 2>/dev/null; then pass "$1"
  else fail "$1" "$3 containing: $2" "$(flat "$(cat "$3" 2>/dev/null)")"; fi
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
# Supplying pipefail to a body production runs without is a FALSE GREEN in the
# direction that matters: a pipeline whose left-hand command fails is graded as
# an abort here and swallowed in CI. So the invocation is read out of the
# workflow, and a step that later gains `shell: bash` moves this harness with
# it. workflow-step.sh REFUSES rather than defaulting when it cannot find the
# step, which is why this is a hard exit and not a fallback.
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

derive_or_die "$SCAN_STEP";    SCAN_SHELL="$DERIVED"
derive_or_die "$EXTRACT_STEP"; EXTRACT_SHELL="$DERIVED"
derive_or_die "$GIST_STEP";    GIST_SHELL="$DERIVED"
echo "== $WF: derived step invocations =="
echo "   '$SCAN_STEP' -> \`$SCAN_SHELL\`"
echo "   '$EXTRACT_STEP' -> \`$EXTRACT_SHELL\`"
echo "   '$GIST_STEP' -> \`$GIST_SHELL\`"

# ---------------------------------------------------------------------------
# The refusal vocabulary. Every refusal this job can print gets a needle, and
# every arm that drives one asserts the other eight are ABSENT — a shared
# non-zero is satisfied by refusals that all fire together, and by refusals
# that all print the same sentence.
#
# A `case` rather than nine `P_*` variables read through `${!v}`: an indirect
# expansion of a key that does not exist yields the EMPTY string, and an empty
# needle makes `want_absent` pass vacuously — so a typo in a key name would
# have silently deleted an obligation. Here an unknown key is a loud refusal,
# and the self-check below then proves the whole table is usable before any arm
# runs. (shellcheck cannot see indirect reads either and flagged all nine as
# unused, SC2034, which is how this was found.)
REFUSALS='SCANFAIL NOBADGE NOURL NOTRIPLE ESCAPE NOSECRET NOGISTID NOPAYLOAD GISTFAIL'
phrase() {
  case "$1" in
    SCANFAIL)  printf '%s' 'ARS scan FAILED' ;;
    NOBADGE)   printf '%s' 'carries no img.shields.io/badge/ARS line' ;;
    NOURL)     printf '%s' 'could be read out of it' ;;
    NOTRIPLE)  printf '%s' 'does not split into a label, a message and a color' ;;
    ESCAPE)    printf '%s' 'carries a percent-escape this step does not decode' ;;
    NOSECRET)  printf '%s' 'GIST_SECRET is not set in repository secrets' ;;
    NOGISTID)  printf '%s' 'COVERAGE_GIST_ID is not set in repository secrets' ;;
    NOPAYLOAD) printf '%s' 'there is no ARS badge payload to publish' ;;
    GISTFAIL)  printf '%s' 'the ARS badge gist update FAILED' ;;
    *)         echo "FAIL: ars-badge-push_test — no refusal phrase for key '$1'" >&2; return 9 ;;
  esac
  return 0
}

want_only() { # <fired-key> <output>
  local fired="$1" out="$2" k
  want_contains "...naming $fired specifically" "$(phrase "$fired")" "$out"
  for k in $REFUSALS; do
    [[ "$k" == "$fired" ]] && continue
    want_absent "...and not confused with $k" "$(phrase "$k")" "$out"
  done
  return 0
}
want_no_refusal() { # <label> <output>
  local out="$2" k found=""
  for k in $REFUSALS; do
    case "$out" in *"$(phrase "$k")"*) found="$k" ;; esac
  done
  if [[ -z "$found" ]]; then pass "$1"; else fail "$1" "no refusal wording" "it printed $found's :: $(flat "$out")"; fi
  return 0
}

# The table has to be USABLE before anything is graded through it, and the
# property it exists for has to hold STRUCTURALLY rather than case by case: an
# empty needle passes every `want_absent` vacuously, and a needle that is a
# substring of another refusal's makes `want_only` unsatisfiable for one of the
# pair no matter how the workflow is written. Both are the "a verification
# mechanism must fail loudly when it cannot run" rule, and neither is visible
# from an arm.
for k in $REFUSALS; do
  kp=$(phrase "$k") || { echo "FAIL: ars-badge-push_test — refusal key '$k' has no phrase" >&2; exit 1; }
  if [[ -z "$kp" ]]; then
    echo "FAIL: ars-badge-push_test — refusal key '$k' maps to an EMPTY phrase; every want_absent on it would pass vacuously" >&2
    exit 1
  fi
  for j in $REFUSALS; do
    [[ "$j" == "$k" ]] && continue
    jp=$(phrase "$j")
    case "$jp" in
      *"$kp"*)
        echo "FAIL: ars-badge-push_test — $k's needle [$kp] is a substring of $j's [$jp]; the two refusals cannot be told apart" >&2
        exit 1 ;;
    esac
  done
done
pass "the $(printf '%s\n' $REFUSALS | wc -l | tr -d ' ') refusal needles are non-empty and pairwise distinguishable"

# ---------------------------------------------------------------------------
# The harness: a case directory holding exactly what the runner's workspace
# holds at that point, a prelude of stubs, then a step body run under the shell
# that step actually gets. The repo's own tree is never written to.

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

# The `curl` stub. This is the ONLY thing standing between this file and a
# write to the live badges gist — the one the README's Coverage and CodeScene
# badges render from right now — so it is a shell FUNCTION shadowing the real
# binary rather than a PATH shim that a body could route around, and it
# records every call it received. `.curl-calls` existing at all is the witness
# that the request was attempted; its ABSENCE is what the refusal arms assert,
# because "the step refused" and "the step published and we did not look" are
# otherwise the same output.
curl_stub() { # $1 = http code printed, $2 = exit status, $3 = response body
  cat <<STUB
curl() {
  printf '%s\n' "\$*" >>./.curl-calls
  _co_out=""
  _co_prev=""
  for _co_a in "\$@"; do
    if [ "\$_co_prev" = "-o" ]; then _co_out="\$_co_a"; fi
    _co_prev="\$_co_a"
  done
  if [ -n "\$_co_out" ]; then printf '%s' '$3' >"\$_co_out"; fi
  printf '%s' '$1'
  return $2
}
STUB
}

CASE=
new_case() { # <name> -> sets CASE to a fresh, empty directory with an empty prelude
  CASE="$TMP/case-$1"
  rm -rf "$CASE"
  mkdir -p "$CASE" || { echo "FAIL: ars-badge-push_test — cannot create case dir $CASE" >&2; exit 1; }
  : >"$CASE/.prelude"
}
with_ars()  { ars_stub "$1" >>"$CASE/.prelude"; }
with_curl() { curl_stub "$1" "$2" "$3" >>"$CASE/.prelude"; }

run_step() { # <workdir> <shell-string> <body-file> [VAR=value ...] -> sets OUT / ST
  local dir="$1" body="$3" script="$1/.step.sh"
  use_shell "$2"
  { cat "$dir/.prelude"; cat "$body"; } >"$script"
  shift 3
  # No `set +e` guard: THIS file runs under `set -uo pipefail` and deliberately
  # not `-e`, so a non-zero inner status — the expected outcome of most arms
  # below — is data, not an abort. Toggling errexit here would leave it ON for
  # the rest of the file, which is the option-you-cannot-see family the sibling
  # issues (#1633, #1635) are made of.
  OUT=$(cd "$dir" && env "$@" "${STEP_ARGV[@]}" .step.sh 2>&1)
  ST=$?
  return 0
}

want_no_request() { # <label>
  if [[ -e "$CASE/.curl-calls" ]]; then
    fail "$1" "no request to the gist API" "the stub recorded: $(flat "$(cat "$CASE/.curl-calls")")"
  else
    pass "$1"
  fi
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

SCAN_ERR='ars: command failed: no such subcommand'
NEW_URL='https://img.shields.io/badge/ARS-Agent--Ready%209.9%2F10-brightgreen'
NEW_JSON='{"schemaVersion":1,"label":"ARS","message":"Agent-Ready 9.9/10","color":"brightgreen"}'
GIST_ID='stubgistid0123456789abcdef'
badge_output() { # <url> -> a plausible `ars scan --badge` tail
  printf 'Badge\n----------------------------------------\n[![ARS](%s)](https://github.com/ingo-eichhorst/agent-readyness)\n' "$1"
}

# ---------------------------------------------------------------------------
# A1 / A2 / A3 — the "Run ARS scan" step. Unchanged by #1654 except for one
# sentence of its refusal, which used to claim README.md was not updated and
# now names the gist.
echo ""
echo "== $WF: $SCAN_STEP =="

# The pre-#1644 body, verbatim. Committed here rather than quoted in an issue,
# per AGENTS.md: "a number which documents behaviour but is not produced by it
# drifts silently".
cat >"$TMP/pre1644-scan.sh" <<'OLD'
ars scan ./core --no-llm --badge > ars-output.txt 2>&1 || true
cat ars-output.txt
OLD

new_case pre1644-scan
with_ars 1
printf '%s\n' "$SCAN_ERR" >"$CASE/.ars-stdout"
run_step "$CASE" "$SCAN_SHELL" "$TMP/pre1644-scan.sh"
if [[ "$ST" -eq 0 ]]; then
  pass "A1: the old scan body still exits 0 with a failing \`ars\` — the hazard is real"
else
  fail "A1: the old scan body exits 0 with a failing \`ars\` (the hazard this pins)" \
       "exit 0" "exit $ST — the hazard is GONE, so re-derive the fix rather than trusting it :: $(flat "$OUT")"
fi

# A floor of 2, not of "however many lines the fixed step has": the pre-#1644
# body was two lines, so a higher floor would report the mutation that RESTORES
# it as "the scan has gone blind" rather than as A2 going red — a negative
# result for the wrong reason reads as coverage it is not.
if extract_body "$SCAN_STEP" 2 "$TMP/scan-body.sh"; then
  new_case scanfail
  with_ars 3
  printf '%s\n' "$SCAN_ERR" >"$CASE/.ars-stdout"
  run_step "$CASE" "$SCAN_SHELL" "$TMP/scan-body.sh"
  want_nonzero "A2: a failing \`ars scan\` fails the step" "$ST" "$OUT"
  want_contains "...as a workflow error annotation, so it surfaces on the run" "::error::" "$OUT"
  want_only SCANFAIL "$OUT"
  want_contains "...and that the badge was therefore not updated" "NOT updated" "$OUT"
  # Without this the failure has no diagnosis anywhere: `ars`'s own message is
  # redirected into the file, so a step that exits 1 without printing it is a
  # red X over an empty log.
  want_contains "...with the scan's own output still in the log" "$SCAN_ERR" "$OUT"

  new_case scanok
  with_ars 0
  badge_output "$NEW_URL" >"$CASE/.ars-stdout"
  run_step "$CASE" "$SCAN_SHELL" "$TMP/scan-body.sh"
  want_status "A3: a succeeding \`ars scan\` still passes" 0 "$ST" "$OUT"
  want_no_refusal "...with no refusal wording anywhere" "$OUT"
  want_file_contains "...having captured the scan output for the next step" "$NEW_URL" "$CASE/ars-output.txt"

  # `|| true` is the false fix for this family and is refused by name — it is
  # what the step used to carry. Full-line comments are stripped first, and
  # that is not tidiness: the step's prose EXPLAINS why it is not used, so a
  # raw scan fails on the sentence saying the right thing. Measured — it did.
  code=$(grep -v '^[[:space:]]*#' "$TMP/scan-body.sh")
  want_absent "the scan step does not reach for \`|| true\`" "|| true" "$code"
  want_contains "...checked against the step's real code, not an empty strip" "ars scan" "$code"
fi

# ---------------------------------------------------------------------------
# B1-B6 — the "Extract ARS badge" step. Before #1654 this step `sed -i`'d the
# score into README.md; it now builds the shields.io endpoint payload the gist
# step publishes. The obligations are #1644's, adapted: same three-outcomes
# discipline, a different artifact, plus the two refusals the decode adds.
echo ""
echo "== $WF: $EXTRACT_STEP =="

# The pre-#1644 extract body, verbatim. Only its silent-SKIP path is reachable
# here (an ars-output.txt with no badge in it), and that is the whole hazard:
# `if [ -n "$ARS_BADGE" ]` with no `else` answered "I could not look" with the
# same bytes as "there was nothing to do". Its README-rewriting half went with
# the mechanism #1654 deleted and is not replayed.
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

new_case pre1644-extract
printf '%s\n' "$SCAN_ERR" >"$CASE/ars-output.txt"
run_step "$CASE" "$EXTRACT_SHELL" "$TMP/pre1644-extract.sh"
want_status "B1: the old extract body over an error output still exits 0 — the hazard is real" 0 "$ST" "$OUT"
want_no_refusal "...and says nothing about it" "$OUT"

if extract_body "$EXTRACT_STEP" 8 "$TMP/extract-body.sh"; then
  # B2, the vacuity guard for B3-B6: a fix that refuses everything is
  # indistinguishable from one that works until something still works.
  new_case extractok
  badge_output "$NEW_URL" >"$CASE/ars-output.txt"
  run_step "$CASE" "$EXTRACT_SHELL" "$TMP/extract-body.sh"
  want_status "B2: a normal run still produces a badge payload" 0 "$ST" "$OUT"
  want_no_refusal "...with no refusal wording anywhere" "$OUT"
  if [[ -s "$CASE/ars-badge.json" ]]; then
    pass "...having written ars-badge.json"
    built=$(jq -Sc . "$CASE/ars-badge.json" 2>&1)
    wanted=$(printf '%s' "$NEW_JSON" | jq -Sc . 2>&1)
    if [[ "$built" == "$wanted" ]]; then
      pass "...as the shields.io endpoint payload for the scanned score ($built)"
    else
      fail "the payload is the endpoint schema for the scanned score" "$wanted" "$built"
    fi
  else
    fail "B2: a normal run still produces a badge payload" "a non-empty ars-badge.json" "missing or empty :: $(flat "$OUT")"
  fi
  # The decode is the reason this step is more than a copy: shields.io's
  # static-badge PATH escapes `-` as `--` and spaces as `%20`, while the
  # endpoint schema takes plain text. A step that published the path's bytes
  # would render `Agent--Ready%209.9%2F10` at every reader, forever, greenly.
  want_file_contains "...with shields.io's path escaping undone, not published raw" \
                     '"message":"Agent-Ready 9.9/10"' "$CASE/ars-badge.json"

  # B3 — output with no badge line at all.
  new_case nobadge
  printf '%s\n' "$SCAN_ERR" >"$CASE/ars-output.txt"
  run_step "$CASE" "$EXTRACT_SHELL" "$TMP/extract-body.sh"
  want_nonzero "B3: output with no badge line fails the step" "$ST" "$OUT"
  want_contains "...as a workflow error annotation" "::error::" "$OUT"
  want_only NOBADGE "$OUT"
  want_contains "...and printing the output it could not read a badge out of" "$SCAN_ERR" "$OUT"

  # B4 — a badge line whose URL cannot be read.
  new_case nourl
  printf '[![ARS](img.shields.io/badge/ARS-unparseable)](x)\n' >"$CASE/ars-output.txt"
  run_step "$CASE" "$EXTRACT_SHELL" "$TMP/extract-body.sh"
  want_nonzero "B4: a badge line with no extractable URL fails the step" "$ST" "$OUT"
  want_only NOURL "$OUT"

  # B5 — the "empty badge value" case: a URL that yields no message or no
  # color. Before #1654 the URL travelled as one opaque string and an empty
  # score could not be seen at all.
  new_case notriple
  badge_output 'https://img.shields.io/badge/ARS-' >"$CASE/ars-output.txt"
  run_step "$CASE" "$EXTRACT_SHELL" "$TMP/extract-body.sh"
  want_nonzero "B5: a badge URL with an empty message fails the step" "$ST" "$OUT"
  want_only NOTRIPLE "$OUT"
  if [[ -e "$CASE/ars-badge.json" ]]; then
    fail "...and wrote no payload for the next step to publish" "no ars-badge.json" "$(flat "$(cat "$CASE/ars-badge.json")")"
  else
    pass "...and wrote no payload for the next step to publish"
  fi

  # B6 — an escape the decode does not model. A guess here is published as
  # fact and read by everyone who looks at the README.
  new_case escape
  badge_output 'https://img.shields.io/badge/ARS-Agent%3AReady-green' >"$CASE/ars-output.txt"
  run_step "$CASE" "$EXTRACT_SHELL" "$TMP/extract-body.sh"
  want_nonzero "B6: a percent-escape the step cannot decode fails the step" "$ST" "$OUT"
  want_only ESCAPE "$OUT"

  code=$(grep -v '^[[:space:]]*#' "$TMP/extract-body.sh")
  # The step's two `|| echo ""` are deliberate and stay — they keep an empty
  # capture readable so the named refusal can report it, rather than letting
  # errexit abort with a line number. `|| true` is the different thing, and the
  # one this family keeps having to refuse.
  want_absent "the extract step does not reach for \`|| true\`" "|| true" "$code"
  want_contains "...checked against the step's real code, not an empty strip" "ars-badge.json" "$code"
  # ...and it no longer touches README.md at all, which is the whole of #1654.
  want_absent "...and it no longer rewrites README.md" "README.md" "$code"
fi

# ---------------------------------------------------------------------------
# C1-C6 — the "Update Gist with ARS score" step. All new in #1654, so every
# obligation here passes the moment it is written: each one was seen red by a
# deliberate mutation of the step before landing.
echo ""
echo "== $WF: $GIST_STEP =="

if extract_body "$GIST_STEP" 8 "$TMP/gist-body.sh"; then
  # C1 — the happy path, driven END TO END: the extract step writes
  # ars-badge.json and the gist step publishes THAT file, so the two steps'
  # only contract is exercised rather than assumed.
  new_case gistok
  with_curl 200 0 '{"html_url":"https://gist.github.com/stub"}'
  badge_output "$NEW_URL" >"$CASE/ars-output.txt"
  run_step "$CASE" "$EXTRACT_SHELL" "$TMP/extract-body.sh"
  want_status "the extract step ran, so the gist step has a real payload to publish" 0 "$ST" "$OUT"
  run_step "$CASE" "$GIST_SHELL" "$TMP/gist-body.sh" GIST_SECRET=stub-token "COVERAGE_GIST_ID=$GIST_ID"
  want_status "C1: a 200 from the gist API passes the step" 0 "$ST" "$OUT"
  want_no_refusal "...with no refusal wording anywhere" "$OUT"
  want_contains "...saying what it published" "Published ARS badge to gist: Agent-Ready 9.9/10" "$OUT"
  if [[ -s "$CASE/.curl-calls" ]]; then
    pass "...having actually issued the request (the stub recorded it)"
    call=$(cat "$CASE/.curl-calls")
    want_contains "...as a PATCH" "-X PATCH" "$call"
    want_contains "...to the gist the secret names" "https://api.github.com/gists/$GIST_ID" "$call"
    want_contains "...carrying the token" "Authorization: token stub-token" "$call"
  else
    fail "C1: the gist step issued a request" "a recorded curl call" "none — the step went green without publishing anything"
  fi
  published=$(jq -Sc '.files["ars.json"].content | fromjson' "$CASE/gist-payload.json" 2>&1)
  wanted=$(printf '%s' "$NEW_JSON" | jq -Sc . 2>&1)
  if [[ "$published" == "$wanted" ]]; then
    pass "...publishing the endpoint payload as ars.json ($published)"
  else
    fail "the request body carries the endpoint payload as ars.json" "$wanted" "$published"
  fi

  # C2 — an unset GIST_SECRET.
  new_case nosecret
  with_curl 200 0 '{}'
  printf '%s\n' "$NEW_JSON" >"$CASE/ars-badge.json"
  run_step "$CASE" "$GIST_SHELL" "$TMP/gist-body.sh" GIST_SECRET= "COVERAGE_GIST_ID=$GIST_ID"
  want_nonzero "C2: an unset GIST_SECRET fails the step" "$ST" "$OUT"
  want_contains "...as a workflow error annotation" "::error::" "$OUT"
  want_only NOSECRET "$OUT"
  want_no_request "...and nothing was sent to the gist API"

  # C3 — an unset COVERAGE_GIST_ID.
  new_case nogistid
  with_curl 200 0 '{}'
  printf '%s\n' "$NEW_JSON" >"$CASE/ars-badge.json"
  run_step "$CASE" "$GIST_SHELL" "$TMP/gist-body.sh" GIST_SECRET=stub-token COVERAGE_GIST_ID=
  want_nonzero "C3: an unset COVERAGE_GIST_ID fails the step" "$ST" "$OUT"
  want_only NOGISTID "$OUT"
  want_no_request "...and nothing was sent to the gist API"

  # C4 — no payload, in both its shapes: the file is absent, and the file is
  # there with an empty message. Both are "the badge value is empty", and both
  # must be refused BEFORE anything is sent — publishing a blank badge is the
  # silent-wrong-score failure this issue is about, in a new spelling.
  new_case nopayload
  with_curl 200 0 '{}'
  run_step "$CASE" "$GIST_SHELL" "$TMP/gist-body.sh" GIST_SECRET=stub-token "COVERAGE_GIST_ID=$GIST_ID"
  want_nonzero "C4: a missing ars-badge.json fails the step" "$ST" "$OUT"
  want_only NOPAYLOAD "$OUT"
  want_no_request "...and nothing was sent to the gist API"

  new_case emptymessage
  with_curl 200 0 '{}'
  printf '%s\n' '{"schemaVersion":1,"label":"ARS","message":"","color":"green"}' >"$CASE/ars-badge.json"
  run_step "$CASE" "$GIST_SHELL" "$TMP/gist-body.sh" GIST_SECRET=stub-token "COVERAGE_GIST_ID=$GIST_ID"
  want_nonzero "C4: an empty badge message fails the step" "$ST" "$OUT"
  want_only NOPAYLOAD "$OUT"
  want_no_request "...and no blank badge was published"

  # C5 — the API answered, and said no. Both a 4xx and a 5xx, because a guard
  # written as `-ge 400` would pass the first and a guard written as
  # `!= 200` would pass neither, and only one of those is the shape shipped.
  for code_body in '404 {"message":"Not Found"}' '503 {"message":"Service Unavailable"}'; do
    http_code=${code_body%% *}
    body=${code_body#* }
    new_case "http$http_code"
    with_curl "$http_code" 0 "$body"
    printf '%s\n' "$NEW_JSON" >"$CASE/ars-badge.json"
    run_step "$CASE" "$GIST_SHELL" "$TMP/gist-body.sh" GIST_SECRET=stub-token "COVERAGE_GIST_ID=$GIST_ID"
    want_nonzero "C5: HTTP $http_code fails the step" "$ST" "$OUT"
    want_contains "...as a workflow error annotation" "::error::" "$OUT"
    want_only GISTFAIL "$OUT"
    want_contains "...naming the code it got" "HTTP $http_code" "$OUT"
    want_contains "...and printing the API's own answer" "$body" "$OUT"
  done

  # C6 — the API was never reached. curl writes "000" and exits non-zero; under
  # this step's implicit `-e` an uncaptured status would abort the step right
  # there, with no annotation and no diagnosis — "could not be attempted" going
  # silent, which is the outcome #1645 exists to keep distinguishable.
  new_case transport
  with_curl 000 7 ''
  printf '%s\n' "$NEW_JSON" >"$CASE/ars-badge.json"
  run_step "$CASE" "$GIST_SHELL" "$TMP/gist-body.sh" GIST_SECRET=stub-token "COVERAGE_GIST_ID=$GIST_ID"
  want_nonzero "C6: a curl that never reached the API fails the step" "$ST" "$OUT"
  want_contains "...as a workflow error annotation" "::error::" "$OUT"
  want_only GISTFAIL "$OUT"
  want_contains "...naming curl's own exit status, not only the code" "curl exit 7" "$OUT"
  want_contains "...and saying there was no answer to print" "no response body" "$OUT"

  # A non-numeric `%{http_code}` must not fall through to the success line: the
  # arithmetic tests would print `integer expression expected` and evaluate
  # FALSE, which is a pass. Committed because that is exactly the shape this
  # file keeps finding — a status read more narrowly than it is produced.
  new_case garbagecode
  with_curl 'not-a-code' 0 '{}'
  printf '%s\n' "$NEW_JSON" >"$CASE/ars-badge.json"
  run_step "$CASE" "$GIST_SHELL" "$TMP/gist-body.sh" GIST_SECRET=stub-token "COVERAGE_GIST_ID=$GIST_ID"
  want_nonzero "C6: an unreadable HTTP code fails the step rather than passing it" "$ST" "$OUT"
  want_only GISTFAIL "$OUT"

  code=$(grep -v '^[[:space:]]*#' "$TMP/gist-body.sh")
  want_absent "the gist step does not reach for \`|| true\`" "|| true" "$code"
  want_contains "...checked against the step's real code, not an empty strip" "api.github.com/gists" "$code"
fi

# ---------------------------------------------------------------------------
# C7 — the job writes NOTHING to the repository.
#
# This is a lock: it passes by construction against the workflow as shipped.
# Per AGENTS.md that is the reason for a deliberate mutation, not an exemption
# from one — so the predicate is a function over a FILE, driven against the
# real workflow (must pass) and against two copies broken in exactly one way
# each (must fail). Without the mutated copies, a predicate that had stopped
# matching would read identically to a clean workflow.
echo ""
echo "== $WF: the job writes nothing to the repository (#1654) =="
writes_to_repo() { # <workflow-file> -> 0 if it grants or performs a repo write
  grep -qE '^[[:space:]]*contents:[[:space:]]*write' "$1" && return 0
  grep -qE '^[[:space:]]*git[[:space:]]+push' "$1" && return 0
  return 1
}
if writes_to_repo "$WF"; then
  fail "the badge job holds no repo write" \
       "no \`contents: write\` and no \`git push\` in $WF" \
       "it has one — that is #1654: the push is refused by the Protect Main ruleset on every run"
else
  pass "the badge job holds no \`contents: write\` and pushes nothing"
fi
sed 's/^\( *\)contents: read/\1contents: write/' "$WF" >"$TMP/mutant-write.yml"
if ! grep -qE '^[[:space:]]*contents:[[:space:]]*write' "$TMP/mutant-write.yml"; then
  fail "the \`contents: write\` mutation applied" "a mutated copy carrying it" "the sed changed nothing — the arm below graded the unmutated file"
elif writes_to_repo "$TMP/mutant-write.yml"; then
  pass "...and a copy that regained \`contents: write\` is caught"
else
  fail "a copy that regained \`contents: write\` is caught" "a detection" "it passed — this check has gone blind"
fi
{ cat "$WF"; printf '          git push\n'; } >"$TMP/mutant-push.yml"
if writes_to_repo "$TMP/mutant-push.yml"; then
  pass "...and a copy that regained a \`git push\` is caught"
else
  fail "a copy that regained a \`git push\` is caught" "a detection" "it passed — this check has gone blind"
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
