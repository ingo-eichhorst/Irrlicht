#!/usr/bin/env bash
# gist-badge-guards_test.sh — the lock on the OTHER two gist-writing badge
# steps: `.github/workflows/coverage.yml`'s "Update Gist with coverage" and
# `.github/workflows/codescene-badge.yml`'s "Update Gist with code health
# score" (#1710).
#
# ---------------------------------------------------------------------------
# Why this is a second file rather than a third arm of ars-badge-push_test.sh
#
# That file's own header states the rule it was written under: "One harness per
# workflow rather than one per step, because the steps share a workflow file, a
# `workflow_step_shell` derivation, an assertion vocabulary and one preflight
# trigger; splitting them would duplicate all four and let the copies
# disagree." Folding two more workflows into it would break the first half of
# that sentence while its filename already fails to describe its contents.
#
# The same rule, applied here, produces ONE file for TWO workflows rather than
# two files: the unit these two share is not a workflow file, it is the
# gist-write SHAPE — one `run:` step each, identical line for line apart from
# the badge's name, its gist filename and the variable it renders. They share
# the curl stub, the refusal vocabulary, the "nothing was sent" witness and
# (after #1710 widens it) one preflight trigger. Two files would duplicate all
# four and let the copies disagree, which is the outcome the rule exists to
# avoid.
#
# What is genuinely shared with ars-badge-push_test.sh is
# `tools/lib/workflow-step.sh`, and that IS shared — the extraction and the
# shell derivation both come from it, so no harness here decides for itself
# what invocation a step gets. The ~70 lines of local harness (`want_*`,
# `run_step`, the stubs) are duplicated deliberately: extracting them would
# rewrite a file merged one commit ago, whose ten obligations would then all
# need replaying as locks on the extraction, for no property either file gains.
#
# ---------------------------------------------------------------------------
# What #1710 was
#
# Both steps declare no `shell:`, so the runner gives them `bash -e {0}` —
# errexit ONLY, no pipefail (see tools/lib/workflow-step.sh; the invocation is
# DERIVED below, never typed here). Two defects followed from that, and both
# were live on `main` while the badges they publish were the two that worked.
#
#   1. A TRANSPORT failure exited with no `::error::` at all. Under errexit an
#      assignment from a failing command substitution aborts the step, so
#      `http_code=$(curl …)` never reached the `echo` or the refusal below it.
#      Measured in the step's own derived shell: exit 6, no `HTTP …` line, no
#      annotation. Loud but undiagnosed (#1702's shape). The comment sitting
#      above that call documented curl's `000` code as though the refusal read
#      it — a comment describing a branch errexit made unreachable, which is
#      the reason nobody re-checked it.
#   2. A NON-NUMERIC `http_code` reported SUCCESS. `[ "" -lt 200 ]` errors and
#      evaluates FALSE, and so does the other disjunct, so execution fell
#      through to the success path. Errexit does not save it — a failing
#      command in an `if` condition is exempt. Measured: two `integer
#      expression expected` lines and exit 0, i.e. the job reporting the badge
#      published on a run where the gist was never written. #1654's own failure
#      class — a silently frozen badge behind a green job — one workflow over.
#
# The fix is `.github/workflows/ars.yml`'s step, ported rather than redesigned:
# `|| curl_status=$?` around the capture, and a `case`-based numeric-shape
# check on `%{http_code}` before it is compared. The three gist-writing
# workflows now carry one shape.
#
# ---------------------------------------------------------------------------
# The obligations, per workflow. G1-G6 are all NEW guards or NEW wording, so
# every one of them passes the moment it is written — each was seen red by a
# deliberate mutation of the real workflow file before landing, and G7 is the
# committed, permanently re-measured half of that evidence.
#
#   G1. the step publishes: a 200 is green, the PATCH went to
#       api.github.com/gists/<id> with the token, the payload carries the
#       badge's own gist filename and endpoint JSON, and the step says what it
#       published. The vacuity guard for G2-G6 — a step replaced by `exit 1`
#       satisfies every refusal arm perfectly.
#   G2. an unset GIST_SECRET is a named failure, and nothing is sent.
#   G3. an unset COVERAGE_GIST_ID is a named failure, and nothing is sent.
#   G4. a non-2xx answer is a named failure naming the code, with the API's own
#       body in the log. Driven at 404 AND 503, because a guard written
#       `-ge 400` passes the first and one written `!= 200` passes neither.
#   G5. a curl that never reached the API is a named failure that names curl's
#       own exit status and says there was no answer to print — defect 1.
#   G6. a non-numeric `%{http_code}` is a named failure and does NOT reach the
#       success line — defect 2. Asserted as the ABSENCE of the success line,
#       because that line is what the pre-fix body printed nothing instead of
#       and a status arm alone cannot tell "refused" from "fell through".
#   G7. the PRE-FIX body, verbatim, re-MEASURED rather than described: both
#       hazards still reproduce in the step's own derived shell. The permanent
#       vacuity guard for G5 and G6 — if errexit ever stopped aborting on a
#       failed capture, or `[` ever stopped evaluating a bad integer as false,
#       the guards above would be protecting nothing and would pass for the
#       wrong reason.
#
# EVERY refusal additionally asserts that no OTHER refusal's wording is
# present — all six, across both workflows, not only the three of the workflow
# under test. A shared non-zero exit is satisfied by refusals that all fire
# together, and by refusals that print the same sentence (#1677/#1678); the
# cross-workflow half also catches the copy-paste this whole class came from,
# a step naming the other badge's file.
#
# ---------------------------------------------------------------------------
# Two things about the harness that are load-bearing
#
# `curl` is shadowed by a shell FUNCTION, never a PATH shim a body could route
# around, and this file is the only thing standing between it and the LIVE
# badges gist — the one README's Coverage, CodeScene and ARS badges all render
# from right now. A bad write breaks three badges at once. So: every arm
# installs the stub, `with_curl` refuses if the stub did not actually land in
# the prelude, `.curl-calls` existing is the witness that a request was
# attempted (its ABSENCE is what the refusal arms assert, since "the step
# refused" and "the step published and we did not look" are otherwise the same
# output), and one final sweep asserts that no recorded call anywhere names the
# real gist id — with a positive control, since a sweep that found no calls at
# all would pass it vacuously.
#
# The COMMITTED PRE-FIX bodies write to `/tmp/gist-payload.json` and
# `/tmp/gist-response.json`, because that is what they did; they are verbatim
# and are not going to be edited to suit a harness. That they cannot be
# isolated per case is one of the reasons the fix moved both steps to
# workspace-relative paths, which is also what ars.yml already used. Nothing in
# this file reads those two paths, so the two pre-fix arms cannot contaminate
# each other through them.
set -uo pipefail

REPO_ROOT=$(git rev-parse --show-toplevel)
cd "$REPO_ROOT" || { echo "FAIL: gist-badge-guards_test — cannot cd to $REPO_ROOT" >&2; exit 1; }

# A missing tool is a hard failure, not a skip: exiting 0 here would read as a
# PASS to the runner that drives this file, so it would go green having
# asserted nothing.
need() { command -v "$1" >/dev/null 2>&1 || { echo "FAIL: gist-badge-guards_test — $1 not found" >&2; exit 1; }; }
need git
need mktemp
need awk
need sed
need grep
need bash
need tr
# jq is not stubbed: the payload these steps build has to be REAL JSON, and
# only jq can say so. `curl` deliberately is NOT needed — it is shadowed by a
# shell function in every arm below, so nothing here can reach the network.
need jq

# shellcheck source=workflow-step.sh
. tools/lib/workflow-step.sh

# The live badges gist. Public (it is in README's three badge URLs), and named
# here so the sweep at the end can prove no arm ever addressed it.
LIVE_GIST_ID='9f14c8e5f25c1ccf5d6500c1685fd9fb'

TMP=$(mktemp -d -t gist-badge-guards) || exit 1
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

# ---------------------------------------------------------------------------
# The two workflows, as data. An unknown key is a loud refusal rather than an
# empty string, for the reason the refusal table below spells out: an empty
# value makes an assertion pass vacuously, so a typo would silently delete an
# obligation instead of failing.
KEYS='codescene coverage'

wf_file() {
  case "$1" in
    codescene) printf '%s' '.github/workflows/codescene-badge.yml' ;;
    coverage)  printf '%s' '.github/workflows/coverage.yml' ;;
    *) echo "FAIL: gist-badge-guards_test — no workflow file for key '$1'" >&2; return 9 ;;
  esac
  return 0
}
wf_step() {
  case "$1" in
    codescene) printf '%s' 'Update Gist with code health score' ;;
    coverage)  printf '%s' 'Update Gist with coverage' ;;
    *) echo "FAIL: gist-badge-guards_test — no step name for key '$1'" >&2; return 9 ;;
  esac
  return 0
}
# The badge inputs the earlier steps of each job put into $GITHUB_ENV. The
# values are the ones the two badges render as this is written, so a failure
# message reads like the real thing.
wf_vars() {
  case "$1" in
    codescene) printf '%s\n' 'SCORE=8.6' 'COLOR=brightgreen' ;;
    coverage)  printf '%s\n' 'COVERAGE=78.7' 'COLOR=yellow' ;;
    *) echo "FAIL: gist-badge-guards_test — no job env for key '$1'" >&2; return 9 ;;
  esac
  return 0
}
wf_gistfile() {
  case "$1" in
    codescene) printf '%s' 'codescene.json' ;;
    coverage)  printf '%s' 'coverage.json' ;;
    *) echo "FAIL: gist-badge-guards_test — no gist filename for key '$1'" >&2; return 9 ;;
  esac
  return 0
}
wf_payload() {
  case "$1" in
    codescene) printf '%s' '{"schemaVersion":1,"label":"code health","message":"8.6/10","color":"brightgreen"}' ;;
    coverage)  printf '%s' '{"schemaVersion":1,"label":"coverage","message":"78.7%","color":"yellow"}' ;;
    *) echo "FAIL: gist-badge-guards_test — no expected payload for key '$1'" >&2; return 9 ;;
  esac
  return 0
}
wf_success() {
  case "$1" in
    codescene) printf '%s' 'Published code health badge to gist: 8.6/10' ;;
    coverage)  printf '%s' 'Published coverage badge to gist: 78.7%' ;;
    *) echo "FAIL: gist-badge-guards_test — no success line for key '$1'" >&2; return 9 ;;
  esac
  return 0
}
wf_prefix() { printf '%s' "$1" | tr '[:lower:]' '[:upper:]'; }

# ---------------------------------------------------------------------------
# The refusal vocabulary — every refusal these two steps can print, across BOTH
# workflows. A `case` rather than six variables read through `${!v}`: an
# indirect expansion of a key that does not exist yields the EMPTY string, and
# an empty needle makes `want_absent` pass vacuously, so a typo in a key name
# would silently delete an obligation.
REFUSALS='CODESCENE_NOSECRET CODESCENE_NOGISTID CODESCENE_GISTFAIL COVERAGE_NOSECRET COVERAGE_NOGISTID COVERAGE_GISTFAIL'
phrase() {
  case "$1" in
    CODESCENE_NOSECRET) printf '%s' 'so the code health badge could not be published at all' ;;
    CODESCENE_NOGISTID) printf '%s' 'there is no gist for the code health badge to be published to' ;;
    CODESCENE_GISTFAIL) printf '%s' 'the code health badge gist update FAILED' ;;
    COVERAGE_NOSECRET)  printf '%s' 'so the coverage badge could not be published at all' ;;
    COVERAGE_NOGISTID)  printf '%s' 'there is no gist for the coverage badge to be published to' ;;
    COVERAGE_GISTFAIL)  printf '%s' 'the coverage badge gist update FAILED' ;;
    *) echo "FAIL: gist-badge-guards_test — no refusal phrase for key '$1'" >&2; return 9 ;;
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
# pair no matter how the workflows are written. Neither is visible from an arm.
echo "== the refusal table =="
for k in $REFUSALS; do
  kp=$(phrase "$k") || { echo "FAIL: gist-badge-guards_test — refusal key '$k' has no phrase" >&2; exit 1; }
  if [[ -z "$kp" ]]; then
    echo "FAIL: gist-badge-guards_test — refusal key '$k' maps to an EMPTY phrase; every want_absent on it would pass vacuously" >&2
    exit 1
  fi
  for j in $REFUSALS; do
    [[ "$j" == "$k" ]] && continue
    jp=$(phrase "$j")
    case "$jp" in
      *"$kp"*)
        echo "FAIL: gist-badge-guards_test — $k's needle [$kp] is a substring of $j's [$jp]; the two refusals cannot be told apart" >&2
        exit 1 ;;
    esac
  done
done
pass "the $(printf '%s\n' $REFUSALS | wc -l | tr -d ' ') refusal needles are non-empty and pairwise distinguishable"

# ...and the same for the two success lines, which G6 asserts the ABSENCE of.
cs_ok=$(wf_success codescene); cov_ok=$(wf_success coverage)
if [[ -z "$cs_ok" || -z "$cov_ok" || "$cs_ok" == "$cov_ok" ]]; then
  echo "FAIL: gist-badge-guards_test — the two success lines are empty or identical; G6's absence arm would pass vacuously" >&2
  exit 1
fi
pass "the two success lines are non-empty and distinct"

# ---------------------------------------------------------------------------
# The harness: a case directory holding exactly what the runner's workspace
# holds at that point, a prelude of stubs, then a step body run under the shell
# that step actually gets. The repo's own tree is never written to.

# The `curl` stub. See the header — this is the only thing between this file
# and the live badges gist.
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
  mkdir -p "$CASE" || { echo "FAIL: gist-badge-guards_test — cannot create case dir $CASE" >&2; exit 1; }
  : >"$CASE/.prelude"
}
# The stub is asserted to have LANDED, not merely to have been requested: a
# prelude that silently stopped defining `curl()` would let a body reach the
# real binary, which is the one outcome this file must make impossible.
with_curl() { # <http-code> <exit-status> <response-body>
  curl_stub "$1" "$2" "$3" >>"$CASE/.prelude"
  if ! grep -q '^curl() {' "$CASE/.prelude"; then
    echo "FAIL: gist-badge-guards_test — the curl stub did not land in $CASE/.prelude; a body would reach the REAL curl" >&2
    exit 1
  fi
}

STEP_ARGV=()
run_step() { # <workdir> <shell-string> <body-file> [VAR=value ...] -> sets OUT / ST
  local dir="$1" body="$3" script="$1/.step.sh"
  read -r -a STEP_ARGV <<<"$2"
  { cat "$dir/.prelude"; cat "$body"; } >"$script"
  shift 3
  # No `set +e` guard: THIS file runs under `set -uo pipefail` and deliberately
  # not `-e`, so a non-zero inner status — the expected outcome of most arms
  # below — is data, not an abort.
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

extract_body() { # <workflow> <step-name> <min-non-blank-lines> <outfile> -> 0 on success
  local wf="$1" name="$2" min="$3" out="$4" lines
  if ! workflow_step_body "$wf" "$name" >"$out"; then
    fail "the '$name' step body was extracted from $wf" \
         "the step's run: | body" "workflow-step refused (above) — the scan has gone blind, not the step clean"
    return 1
  fi
  lines=$(grep -cve '^[[:space:]]*$' "$out")
  if [[ "${lines:-0}" -lt "$min" ]]; then
    fail "the '$name' step body was extracted from $wf" \
         "at least $min non-blank lines" "$lines — the scan has gone blind, not the step clean"
    return 1
  fi
  pass "extracted the '$name' step body from $wf ($lines non-blank lines)"
  return 0
}

# ---------------------------------------------------------------------------
# The PRE-FIX bodies, verbatim, as they stood on `main` at 326ba009. Committed
# here rather than quoted in an issue, per AGENTS.md: "a number which documents
# behaviour but is not produced by it drifts silently". Re-measured on every
# run by G7.
cat >"$TMP/prefix-codescene.sh" <<'OLD'
if [ -z "$GIST_SECRET" ]; then
  echo "::error::GIST_SECRET is not set in repository secrets"
  exit 1
fi
if [ -z "$COVERAGE_GIST_ID" ]; then
  echo "::error::COVERAGE_GIST_ID is not set in repository secrets"
  exit 1
fi
MESSAGE=$(printf '%.1f/10' "$SCORE")
BADGE_JSON="{\"schemaVersion\":1,\"label\":\"code health\",\"message\":\"${MESSAGE}\",\"color\":\"${COLOR}\"}"
jq -n --arg content "$BADGE_JSON" '{"files":{"codescene.json":{"content":$content}}}' > /tmp/gist-payload.json
# curl writes "000" to %{http_code} when no response was received (DNS/TLS/connect failure).
http_code=$(curl -sS -o /tmp/gist-response.json -w '%{http_code}' \
  --connect-timeout 10 --max-time 30 \
  --retry 3 --retry-delay 2 --retry-all-errors \
  -X PATCH \
  -H "Authorization: token $GIST_SECRET" \
  -H "Content-Type: application/json" \
  -d @/tmp/gist-payload.json \
  "https://api.github.com/gists/$COVERAGE_GIST_ID")
echo "HTTP $http_code"
if [ "$http_code" -lt 200 ] || [ "$http_code" -ge 300 ]; then
  echo "::error::Gist update failed (HTTP $http_code)"
  if [ -s /tmp/gist-response.json ]; then
    cat /tmp/gist-response.json
  else
    echo "(no response body — likely transport failure)"
  fi
  exit 1
fi
OLD

cat >"$TMP/prefix-coverage.sh" <<'OLD'
if [ -z "$GIST_SECRET" ]; then
  echo "::error::GIST_SECRET is not set in repository secrets"
  exit 1
fi
if [ -z "$COVERAGE_GIST_ID" ]; then
  echo "::error::COVERAGE_GIST_ID is not set in repository secrets"
  exit 1
fi
BADGE_JSON="{\"schemaVersion\":1,\"label\":\"coverage\",\"message\":\"${COVERAGE}%\",\"color\":\"${COLOR}\"}"
jq -n --arg content "$BADGE_JSON" '{"files":{"coverage.json":{"content":$content}}}' > /tmp/gist-payload.json
# curl writes "000" to %{http_code} when no response was received (DNS/TLS/connect failure).
http_code=$(curl -sS -o /tmp/gist-response.json -w '%{http_code}' \
  --connect-timeout 10 --max-time 30 \
  --retry 3 --retry-delay 2 --retry-all-errors \
  -X PATCH \
  -H "Authorization: token $GIST_SECRET" \
  -H "Content-Type: application/json" \
  -d @/tmp/gist-payload.json \
  "https://api.github.com/gists/$COVERAGE_GIST_ID")
echo "HTTP $http_code"
if [ "$http_code" -lt 200 ] || [ "$http_code" -ge 300 ]; then
  echo "::error::Gist update failed (HTTP $http_code)"
  if [ -s /tmp/gist-response.json ]; then
    cat /tmp/gist-response.json
  else
    echo "(no response body — likely transport failure)"
  fi
  exit 1
fi
OLD

GIST_ID='stubgistid0123456789abcdef'

# ---------------------------------------------------------------------------
for key in $KEYS; do
  WF=$(wf_file "$key")     || exit 1
  STEP=$(wf_step "$key")   || exit 1
  P=$(wf_prefix "$key")
  GISTFILE=$(wf_gistfile "$key") || exit 1
  WANT_PAYLOAD=$(wf_payload "$key") || exit 1
  OKLINE=$(wf_success "$key") || exit 1
  # The job env each step's earlier steps leave in $GITHUB_ENV, as an array so
  # `run_step` can pass it through `env`.
  JOBENV=()
  while IFS= read -r line; do JOBENV+=("$line"); done < <(wf_vars "$key")
  if [[ "${#JOBENV[@]}" -eq 0 ]]; then
    echo "FAIL: gist-badge-guards_test — no job env for '$key'; every arm below would run against an empty badge value" >&2
    exit 1
  fi

  echo ""
  echo "== $WF: $STEP =="

  # The invocation, DERIVED from the workflow rather than spelled here (#1650).
  # GitHub has two bash invocations: a step DECLARING `shell: bash` gets
  # `bash --noprofile --norc -e -o pipefail {0}`, one declaring nothing gets
  # `bash -e {0}` — no pipefail. Supplying pipefail to a body production runs
  # without is a FALSE GREEN in the direction that matters. workflow-step.sh
  # REFUSES rather than defaulting when it cannot find the step, which is why
  # this is a hard exit and not a fallback.
  if ! GIST_SHELL=$(workflow_step_shell "$WF" "$STEP"); then
    echo "FAIL: gist-badge-guards_test — could not derive the shell $WF gives '$STEP' (refusal above); nothing below would have graded the real program" >&2
    exit 1
  fi
  echo "   derived invocation: \`$GIST_SHELL\`"

  # A floor of 25, not "however many lines the fixed step has": the pre-fix
  # bodies are 29 and 30 non-blank lines, so a higher floor would report the
  # mutation that RESTORES one of them as "the scan has gone blind" rather than
  # as G5/G6 going red — a negative result for the wrong reason reads as
  # coverage it is not.
  BODY="$TMP/body-$key.sh"
  if ! extract_body "$WF" "$STEP" 25 "$BODY"; then
    continue
  fi

  # -- G1: the vacuity guard. A fix that refuses everything is
  # indistinguishable from one that works until something still works.
  new_case "$key-ok"
  with_curl 200 0 '{"html_url":"https://gist.github.com/stub"}'
  run_step "$CASE" "$GIST_SHELL" "$BODY" "${JOBENV[@]}" GIST_SECRET=stub-token "COVERAGE_GIST_ID=$GIST_ID"
  want_status "G1: a 200 from the gist API passes the step" 0 "$ST" "$OUT"
  want_no_refusal "...with no refusal wording anywhere" "$OUT"
  want_contains "...saying what it published" "$OKLINE" "$OUT"
  if [[ -s "$CASE/.curl-calls" ]]; then
    pass "...having actually issued the request (the stub recorded it)"
    call=$(cat "$CASE/.curl-calls")
    want_contains "...as a PATCH" "-X PATCH" "$call"
    want_contains "...to the gist the secret names" "https://api.github.com/gists/$GIST_ID" "$call"
    want_contains "...carrying the token" "Authorization: token stub-token" "$call"
  else
    fail "G1: the step issued a request" "a recorded curl call" "none — the step went green without publishing anything"
  fi
  published=$(jq -Sc ".files[\"$GISTFILE\"].content | fromjson" "$CASE/gist-payload.json" 2>&1)
  wanted=$(printf '%s' "$WANT_PAYLOAD" | jq -Sc . 2>&1)
  if [[ "$published" == "$wanted" ]]; then
    pass "...publishing the endpoint payload as $GISTFILE ($published)"
  else
    fail "the request body carries the endpoint payload as $GISTFILE" "$wanted" "$published"
  fi

  # -- G2: an unset GIST_SECRET.
  new_case "$key-nosecret"
  with_curl 200 0 '{}'
  run_step "$CASE" "$GIST_SHELL" "$BODY" "${JOBENV[@]}" GIST_SECRET= "COVERAGE_GIST_ID=$GIST_ID"
  want_nonzero "G2: an unset GIST_SECRET fails the step" "$ST" "$OUT"
  want_contains "...as a workflow error annotation, so it surfaces on the run" "::error::" "$OUT"
  want_only "${P}_NOSECRET" "$OUT"
  want_no_request "...and nothing was sent to the gist API"

  # -- G3: an unset COVERAGE_GIST_ID.
  new_case "$key-nogistid"
  with_curl 200 0 '{}'
  run_step "$CASE" "$GIST_SHELL" "$BODY" "${JOBENV[@]}" GIST_SECRET=stub-token COVERAGE_GIST_ID=
  want_nonzero "G3: an unset COVERAGE_GIST_ID fails the step" "$ST" "$OUT"
  want_contains "...as a workflow error annotation" "::error::" "$OUT"
  want_only "${P}_NOGISTID" "$OUT"
  want_no_request "...and nothing was sent to the gist API"

  # -- G4: the API answered, and said no. Both a 4xx and a 5xx, because a guard
  # written `-ge 400` passes the first and one written `!= 200` passes neither,
  # and only one of those is the shape shipped.
  for code_body in '404 {"message":"Not Found"}' '503 {"message":"Service Unavailable"}'; do
    http_code=${code_body%% *}
    body=${code_body#* }
    new_case "$key-http$http_code"
    with_curl "$http_code" 0 "$body"
    run_step "$CASE" "$GIST_SHELL" "$BODY" "${JOBENV[@]}" GIST_SECRET=stub-token "COVERAGE_GIST_ID=$GIST_ID"
    want_nonzero "G4: HTTP $http_code fails the step" "$ST" "$OUT"
    want_contains "...as a workflow error annotation" "::error::" "$OUT"
    want_only "${P}_GISTFAIL" "$OUT"
    want_contains "...naming the code it got" "HTTP $http_code" "$OUT"
    want_contains "...and printing the API's own answer" "$body" "$OUT"
    want_absent "...and NOT reporting the badge published" "$OKLINE" "$OUT"
  done

  # -- G5 (defect 1): the API was never reached. curl writes "000" and exits
  # non-zero; before #1710 an uncaptured status aborted the step right here,
  # with no annotation and no diagnosis — "could not be attempted" going
  # silent, which is the outcome #1645 exists to keep distinguishable.
  new_case "$key-transport"
  with_curl 000 7 ''
  run_step "$CASE" "$GIST_SHELL" "$BODY" "${JOBENV[@]}" GIST_SECRET=stub-token "COVERAGE_GIST_ID=$GIST_ID"
  want_nonzero "G5: a curl that never reached the API fails the step" "$ST" "$OUT"
  want_contains "...as a workflow error annotation" "::error::" "$OUT"
  want_only "${P}_GISTFAIL" "$OUT"
  want_contains "...naming curl's own exit status, not only the code" "curl exit 7" "$OUT"
  want_contains "...and reaching the line the abort used to skip" "HTTP 000" "$OUT"
  want_contains "...and saying there was no answer to print" "no response body" "$OUT"
  want_absent "...and NOT reporting the badge published" "$OKLINE" "$OUT"

  # -- G6 (defect 2): a non-numeric %{http_code}. The arithmetic tests would
  # print `integer expression expected`, evaluate FALSE, and fall through to
  # the success line — a failed publish reported as a publish. The ABSENCE of
  # the success line is the discriminating assertion; a status arm alone cannot
  # tell "refused" from "fell through", since both can exit non-zero for other
  # reasons.
  new_case "$key-garbagecode"
  with_curl 'not-a-code' 0 '{}'
  run_step "$CASE" "$GIST_SHELL" "$BODY" "${JOBENV[@]}" GIST_SECRET=stub-token "COVERAGE_GIST_ID=$GIST_ID"
  want_nonzero "G6: an unreadable HTTP code fails the step rather than passing it" "$ST" "$OUT"
  want_only "${P}_GISTFAIL" "$OUT"
  want_absent "...and never reaches the success line" "$OKLINE" "$OUT"
  want_absent "...having decided the code's SHAPE rather than erroring on it" "integer expression expected" "$OUT"

  # -- G7: the pre-fix body, verbatim, re-MEASURED. The permanent vacuity guard
  # for G5 and G6.
  PREFIX_BODY="$TMP/prefix-$key.sh"
  new_case "$key-prefix-transport"
  with_curl 000 7 ''
  run_step "$CASE" "$GIST_SHELL" "$PREFIX_BODY" "${JOBENV[@]}" GIST_SECRET=stub-token "COVERAGE_GIST_ID=$GIST_ID"
  if [[ "$ST" -ne 0 ]]; then
    pass "G7: the pre-fix body still fails on a transport error (exit $ST)"
  else
    fail "G7: the pre-fix body fails on a transport error" "a non-zero exit" "exit 0 :: $(flat "$OUT")"
  fi
  want_absent "...but with NO error annotation — errexit aborted at the capture" "::error::" "$OUT"
  want_absent "...and no HTTP line, so the log says nothing about what happened" "HTTP " "$OUT"

  new_case "$key-prefix-garbagecode"
  with_curl 'not-a-code' 0 '{}'
  run_step "$CASE" "$GIST_SHELL" "$PREFIX_BODY" "${JOBENV[@]}" GIST_SECRET=stub-token "COVERAGE_GIST_ID=$GIST_ID"
  want_status "G7: the pre-fix body reports SUCCESS on an unreadable HTTP code — the hazard is real" 0 "$ST" "$OUT"
  want_absent "...with no error annotation anywhere" "::error::" "$OUT"
  want_contains "...having errored on the comparison and evaluated it FALSE" "integer expression expected" "$OUT"

  # `|| true` is the false fix for this family and is refused by name. Full-line
  # comments are stripped first: the step's prose EXPLAINS the guards, so a raw
  # scan would fail on a sentence saying the right thing.
  code=$(grep -v '^[[:space:]]*#' "$BODY")
  want_absent "the step does not reach for \`|| true\`" "|| true" "$code"
  want_contains "...checked against the step's real code, not an empty strip" "api.github.com/gists" "$code"
  # The `000` claim the old comment made is now true of a branch that runs, and
  # the capture is what makes it so. Both are asserted against the real body,
  # because the fix is one line and a revert would be one line too.
  want_contains "...and the curl status is captured rather than aborting the step" "|| curl_status=" "$code"
  want_contains "...and the code's shape is decided before it is compared" 'http_code_usable' "$code"
done

# ---------------------------------------------------------------------------
# The one assertion that is about this FILE rather than about the workflows:
# not one arm above addressed the live badges gist. Its absence is checked with
# a positive control, because a sweep that found no recorded calls at all would
# pass it having looked at nothing.
echo ""
echo "== nothing reached the live badges gist =="
calls=$(cat "$TMP"/case-*/.curl-calls 2>/dev/null)
if [[ -z "$calls" ]]; then
  fail "the curl stub recorded the requests the arms made" \
       "at least one recorded call" "none — the sweep below would pass having read nothing"
else
  pass "the curl stub recorded $(printf '%s\n' "$calls" | grep -c . ) request(s) across the arms"
  want_absent "...and none of them named the live badges gist" "$LIVE_GIST_ID" "$calls"
  want_absent "...and none of them named gist.githubusercontent.com" "gist.githubusercontent.com" "$calls"
fi

# ---------------------------------------------------------------------------
# ...and preflight's `tools` gate has to FIRE on a diff touching only one of
# these workflows, or under --changed (the pre-push hook's path) every
# assertion above is skipped on precisely the commit that can break it. That is
# #1591's, #1629's, #1639's and #1641's shape, each fixed by widening this same
# trigger. The regex is EXTRACTED and matched rather than string-compared, so
# this is a behavioural assertion and not a lock on one spelling of an
# alternation.
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
    for probe in .github/workflows/codescene-badge.yml .github/workflows/coverage.yml tools/lib/gist-badge-guards_test.sh; do
      if printf '%s\n' "$probe" | grep -qE "$tools_re"; then
        pass "...it fires on a diff touching $probe"
      else
        fail "the tools gate fires on a diff touching $probe" "a match" "no match against: $tools_re"
      fi
    done
    # The vacuity guard: a trigger that matched everything would satisfy every
    # probe above and scope nothing.
    if printf '%s\n' core/domain/session.go | grep -qE "$tools_re"; then
      fail "the tools-gate trigger still scopes" "no match for core/domain/session.go" "it matches everything: $tools_re"
    else
      pass "...and still does not fire on an unrelated core/ file"
    fi
  fi
fi

echo ""
if [[ "$rc" -eq 0 ]]; then
  echo "gist-badge-guards_test: ALL PASS"
else
  echo "gist-badge-guards_test: FAILURES" >&2
fi
exit "$rc"
