#!/usr/bin/env bash
# replaydata-deletion-guard_test.sh — the lock on
# .github/workflows/replaydata-deletion-guard.yml's "Detect deletions of
# load-bearing replaydata" step (#1645).
#
# ---------------------------------------------------------------------------
# What is being asserted, and why each obligation exists
#
# The defect: the step captured its diff as
#
#     deletions=$(git diff --name-only --diff-filter=D --find-renames=50% \
#                   "$base...$head" -- 'replaydata/**' || true)
#
# and then classified `$deletions` into `$violations`, failing iff
# `$violations` was non-empty. `|| true` makes a git that FAILED produce an
# empty `$deletions` — which is this guard's SUCCESS condition. The
# classification loop read nothing, `$violations` stayed empty, and the step
# printed "OK: no disallowed deletions" and exited 0 having examined no diff at
# all. A git exiting 128 was byte-for-byte a clean PR. This is a MERGE GATE, so
# its blind spot permits exactly what it exists to prevent: #268 requires a
# recording to be retired by `git mv` into regression/ and never deleted.
#
# Note WHERE the defect was, because this block was spot-checked and cleared
# twice while the sibling issues were worked (#1639's agent, then #1641's
# brief). Both readings were of the classification LOOP, which is fine — it
# accumulates into `$violations` and reports after the loop. The statement that
# decides the step's status is the loop's INPUT, one line above it.
#
# The obligations, in order:
#
#   1. both pre-fix hazards are re-MEASURED, not described. The `|| true`
#      capture is emitted verbatim and run under the shell GitHub actually
#      gives a step, and the empty-context expansion is run against REAL git.
#      They double as the vacuity guard: if either stopped behaving that way
#      the fix would be protecting nothing and every arm below would pass for
#      the wrong reason.
#   2. the real step — extracted from the real workflow file and EXECUTED —
#      still FAILS on each kind of disallowed deletion. Without this a gate
#      that refuses everything is indistinguishable from one that works.
#   3. a clean diff still passes, and the step is shown to have actually
#      diffed rather than to have exited early.
#   4. a git that FAILS now fails the step, names why, and does NOT print the
#      pass line.
#   5. the permitted cases still pass — orphan recordings, a live cell's
#      assessment.json, a rename, a deletion outside any recording folder.
#      A fix that made the guard refuse more than it should would be reverted
#      by the first person it blocked, so this is not optional politeness.
#   6. the two NEW refusals fire: an empty pull_request context, and a base or
#      head commit this checkout does not have.
#
# Behavioural rather than a text scan, per #1647: a scan pins one spelling of a
# guard where running the block pins the property, and this file then runs on a
# real runner for free because test.yml discovers tools/lib/*_test.sh.
#
# What the stub CANNOT grade, said plainly rather than implied: `git mv` is
# permitted because git's own rename detection reports a rename as R (filtered
# out by --diff-filter=D), not because of anything this step does. Rename
# detection is also ON by default in modern git, so `--find-renames=50%` pins
# the THRESHOLD, not the behaviour. The rename arm therefore models git
# faithfully — the stub reports the old path as a deletion when the invocation
# does NOT ask for rename detection — which grades the invocation the step
# makes rather than claiming the step implements renaming.
set -uo pipefail

REPO_ROOT=$(git rev-parse --show-toplevel)
cd "$REPO_ROOT" || { echo "FAIL: replaydata-deletion-guard_test — cannot cd to $REPO_ROOT" >&2; exit 1; }

# A missing tool is a hard failure, not a skip: exiting 0 here would read as a
# PASS to the runner that drives this file, so it would go green having
# asserted nothing — the failure mode this whole issue family is made of.
need() { command -v "$1" >/dev/null 2>&1 || { echo "FAIL: replaydata-deletion-guard_test — $1 not found" >&2; exit 1; }; }
need git
need mktemp
need awk
need sed
need bash
need wc
need grep

WF=.github/workflows/replaydata-deletion-guard.yml
STEP='Detect deletions of load-bearing replaydata'

# The step's body AND the shell it runs under both come from the workflow file,
# through tools/lib/workflow-step.sh — see the invocation block below.
# shellcheck source=workflow-step.sh
. tools/lib/workflow-step.sh

TMP=$(mktemp -d -t replaydata-deletion-guard) || exit 1
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
# The fixture checkout.
#
# The step's `cell_is_live` reads the WORKING TREE (`[ -f replaydata/agents/
# $a/scenarios/$fold/metadata.json ]`), so "live cell" and "orphan cell" are
# properties of the directory the body runs in, not of the diff. Every body is
# therefore executed with this as its cwd. It is a throwaway tree under
# $TMP — the real replaydata/ catalog is deletion-guarded (by this very
# workflow) and is never touched, read or written by this file.
CHECKOUT="$TMP/checkout"
mkdir -p "$CHECKOUT/replaydata/agents/claudecode/scenarios/1-1_live/recordings/r1"
mkdir -p "$CHECKOUT/replaydata/agents/claudecode/scenarios/9-9_orphan/recordings/r1"
mkdir -p "$CHECKOUT/replaydata/agents/claudecode/regressions/8-8_frozen"
mkdir -p "$CHECKOUT/replaydata/orchestrators/gastown/scenarios"
# The live cell holds a metadata.json at HEAD; the orphan deliberately does not.
echo '{"id":"1-1_live"}' >"$CHECKOUT/replaydata/agents/claudecode/scenarios/1-1_live/metadata.json"

LIVE=replaydata/agents/claudecode/scenarios/1-1_live
ORPHAN=replaydata/agents/claudecode/scenarios/9-9_orphan

# ---------------------------------------------------------------------------
# The invocation, DERIVED from the workflow rather than spelled here (#1650).
#
# GitHub has two bash invocations and this file used to state the wrong one as
# fact: a step DECLARING `shell: bash` gets
# `bash --noprofile --norc -e -o pipefail {0}`, but a step declaring nothing —
# which is what this workflow's step does — gets `bash -e {0}`. No
# `--noprofile`, no `--norc`, and **no pipefail**; measured off run
# 31960152598's own header for THIS step:
#
#     guard  Detect deletions of load-bearing replaydata   shell: /usr/bin/bash -e {0}
#
# The step sets `set -euo pipefail` on its own first line, so what it actually
# runs with is unchanged either way — but the harness must not be the thing
# that supplies it, because then a step that dropped that line would still be
# graded under pipefail here and swallow a mid-pipeline failure in CI. Reading
# the invocation out of the workflow is what keeps the two in step;
# workflow-step.sh REFUSES rather than defaulting when it cannot find the step,
# which is why this is a hard exit and not a fallback.
if ! STEP_SHELL=$(workflow_step_shell "$WF" "$STEP"); then
  echo "FAIL: replaydata-deletion-guard_test — could not derive the shell $WF gives '$STEP' (refusal above); nothing below would have graded the real program" >&2
  exit 1
fi
read -r -a STEP_ARGV <<<"$STEP_SHELL"
echo "== $WF :: '$STEP' runs under \`$STEP_SHELL\` (derived) =="

# ---------------------------------------------------------------------------
# The harness: stubs, then a body, run under that shell.
#
# `git` is a shell FUNCTION rather than a PATH shim so the body's own lines
# survive byte-for-byte. An unmodelled git subcommand returns a loud,
# distinctive 99 naming the call instead of a quiet 0: a stub that silently
# answered "fine" to something it does not model would make every arm below
# pass for a reason unrelated to its obligation.
#
# STUB_ARGV_FILE records the argv of every `git diff` the body performs, so an
# arm can assert the step ACTUALLY DIFFED rather than exited before looking —
# an arm that passes because nothing ran is this issue's own shape one level up.
stub_prelude() { # $1 = base sha, $2 = head sha, $3 = diff mode, $4 = deletions payload
                 # $5 = space-separated list of shas `git rev-parse` must not find
  cat <<STUB
STUB_BASE='$1'
STUB_HEAD='$2'
STUB_DIFF_MODE='$3'
STUB_DELETIONS='$4'
STUB_MISSING=' $5 '
STUB_ARGV_FILE='$TMP/git-diff-argv'
git() {
  case "\$1" in
    rev-parse)
      # The step asks \`git rev-parse --verify --quiet <sha>^{commit}\`.
      sha=\${@: -1}
      sha=\${sha%^\{commit\}}
      case "\$STUB_MISSING" in
        *" \$sha "*) return 1 ;;
      esac
      echo "\$sha"
      return 0 ;;
    diff)
      printf '%s\n' "git \$*" >>"\$STUB_ARGV_FILE"
      case "\$STUB_DIFF_MODE" in
        ok)
          if [ -n "\$STUB_DELETIONS" ]; then printf '%s\n' "\$STUB_DELETIONS"; fi
          return 0 ;;
        fail)
          echo "fatal: bad object \$STUB_BASE" >&2
          return 128 ;;
        rename)
          # Real git: a detected rename is R, which --diff-filter=D drops.
          # WITHOUT rename detection the same \`git mv\` is reported as a
          # deletion of the old path plus an addition of the new one, and
          # --diff-filter=D yields the old path.
          case " \$* " in
            *" --find-renames"*|*" --find-renames="*) return 0 ;;
            *) printf '%s\n' "\$STUB_DELETIONS"; return 0 ;;
          esac ;;
        *) echo "STUB: unmodelled diff mode: \$STUB_DIFF_MODE" >&2; return 99 ;;
      esac ;;
    *) echo "STUB: unmodelled call: git \$*" >&2; return 99 ;;
  esac
}
STUB
}

# run_body <file-with-body> <base> <head> <mode> <deletions> [missing-shas]
# -> sets OUT / ST / ARGV
run_body() {
  local body="$1" script="$TMP/step.sh"
  rm -f "$TMP/git-diff-argv"
  { stub_prelude "$2" "$3" "$4" "$5" "${6:-}"; cat "$body"; } >"$script"
  # No `set +e` guard around this: THIS file runs under `set -uo pipefail` and
  # deliberately not `-e`, so a non-zero inner status — the expected outcome of
  # half the arms below — is data, not an abort. Toggling errexit here would
  # leave it on for the rest of the file, which is the option-you-cannot-see
  # family the sibling issues (#1633, #1635) are made of.
  OUT=$(cd "$CHECKOUT" && "${STEP_ARGV[@]}" "$script" 2>&1)
  ST=$?
  ARGV=$(cat "$TMP/git-diff-argv" 2>/dev/null || true)
  return 0
}

BASE_SHA=1111111111111111111111111111111111111111
HEAD_SHA=2222222222222222222222222222222222222222

# ---------------------------------------------------------------------------
# Obligation 1a — the `|| true` hazard, re-measured on every run.
#
# The pre-#1645 capture, verbatim, with the classification tail trimmed to the
# one case it needs (the two `${{ }}` expressions are the `base=`/`head=`
# lines). Committed here rather than quoted in an issue, per AGENTS.md: "a
# number which documents behaviour but is not produced by it drifts silently".
echo "== the pre-#1645 \`|| true\` capture (the defect, re-measured) =="
cat >"$TMP/predecessor-ortrue.sh" <<'OLD'
set -euo pipefail
base="$STUB_BASE"
head="$STUB_HEAD"
deletions=$(git diff --name-only --diff-filter=D --find-renames=50% "$base...$head" -- 'replaydata/**' || true)
violations=""
while IFS= read -r f; do
  [ -z "$f" ] && continue
  case "$f" in
    replaydata/agents/scenarios.json)
      violations="$violations  - $f  (matrix catalog)"$'\n' ;;
    *) : ;;
  esac
done <<< "$deletions"
if [ -n "$violations" ]; then
  echo "::error::PR deletes load-bearing replaydata (catalog or referenced recordings)."
  exit 1
fi
echo "OK: no disallowed deletions (orphan recordings, assessment.json, and renames are permitted)."
OLD
run_body "$TMP/predecessor-ortrue.sh" "$BASE_SHA" "$HEAD_SHA" fail ""
if [[ "$ST" -eq 0 ]]; then
  pass "the old capture still exits 0 when git FAILS — the hazard is real"
else
  fail "the old \`|| true\` capture exits 0 when git fails (the hazard this pins)" \
       "exit 0" "exit $ST — the hazard is GONE, so re-derive the fix rather than trusting it :: $(flat "$OUT")"
fi
want_contains "...printing its own pass line over a git that fatally failed" \
  "OK: no disallowed deletions" "$OUT"

# ---------------------------------------------------------------------------
# Obligation 1b — the empty-context hazard, measured against REAL git.
#
# `on: workflow_dispatch` has no `github.event.pull_request`, so both `${{ }}`
# expressions expand to the empty string and the diff becomes `git diff "..."`.
# This is not stubbed: a claim about how git parses `...` has to be made by git.
echo ""
echo "== the pre-#1645 empty pull_request context, against real git =="
EMPTYREPO="$TMP/emptyctx"
mkdir -p "$EMPTYREPO/replaydata/agents"
echo '{}' >"$EMPTYREPO/replaydata/agents/scenarios.json"
if ! ( cd "$EMPTYREPO" \
       && git -c init.defaultBranch=main init -q . \
       && git -c user.email=t@example.com -c user.name=t add -A \
       && git -c user.email=t@example.com -c user.name=t commit -qm init ) >/dev/null 2>&1; then
  fail "a throwaway repo could be built for the empty-context measurement" \
       "an initialised repo" "git init/commit failed — the measurement could not run"
else
  e_base=""; e_head=""
  e_out=$( cd "$EMPTYREPO" && git diff --name-only --diff-filter=D --find-renames=50% "$e_base...$e_head" -- 'replaydata/**' 2>&1 )
  e_st=$?
  if [[ "$e_st" -eq 0 && -z "$e_out" ]]; then
    pass "real git reads \`...\` as HEAD...HEAD: exit 0, no output — so the pre-fix step PASSED on workflow_dispatch"
  else
    fail "real git answers an empty base...head with a silent success (the hazard this pins)" \
         "exit 0 and no output" "exit $e_st :: $(flat "$e_out") — the hazard is GONE, so re-derive the refusal rather than trusting it"
  fi
fi

# ---------------------------------------------------------------------------
# Obligations 2-6 — the REAL step, extracted from the REAL workflow and run.
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
  if ! workflow_step_body "$WF" "$STEP" >"$TMP/step-body-raw.sh"; then
    fail "the '$STEP' step body was extracted from $WF" \
         "the step's run: | body" "workflow-step refused (above) — the scan has gone blind, not the step clean"
    : >"$TMP/step-body-raw.sh"
  fi

  # The extraction has to have found something. A scan that silently stopped
  # matching reads exactly like a workflow with no hazard in it, and every arm
  # below would then grade an empty file — which exits 0 and looks like a clean
  # PR. The floor is far below the body's real size: the point is to catch a
  # scan that returned nothing or a fragment, not to pin a line count.
  lines=$(grep -cve '^[[:space:]]*$' "$TMP/step-body-raw.sh")
  if [[ "${lines:-0}" -lt 30 ]]; then
    fail "the '$STEP' step body was extracted from $WF" \
         "at least 30 non-blank lines" "$lines — the scan has gone blind, not the step clean"
  else
    pass "extracted the '$STEP' step body from $WF ($lines non-blank lines)"

    # ...and the WHOLE step, not just its head. The classification loop and the
    # pass line live at the far end of the block; an extractor that stopped at
    # the first dedent would still clear the line floor above.
    body_raw=$(cat "$TMP/step-body-raw.sh")
    want_contains "...through to the classification loop" 'violations=' "$body_raw"
    want_contains "...and through to the step's own pass line" 'OK: examined' "$body_raw"

    # Substitute the two workflow expressions the runner would have expanded.
    # Nothing else is touched — this is the real body.
    sed -e 's|\${{ github\.event\.pull_request\.base\.sha }}|$STUB_BASE|g' \
        -e 's|\${{ github\.event\.pull_request\.head\.sha }}|$STUB_HEAD|g' \
        "$TMP/step-body-raw.sh" >"$TMP/step-body.sh"
    body=$(cat "$TMP/step-body.sh")
    want_contains "the base expression was substituted" 'base="$STUB_BASE"' "$body"
    want_contains "the head expression was substituted" 'head="$STUB_HEAD"' "$body"
    # If an expression were respelled, the sed would match nothing and the body
    # would still carry `${{`, which bash rejects as a bad substitution — a
    # confusing failure attributed to the wrong thing. Name it here instead.
    want_absent "...leaving no unexpanded \${{ }} expression behind" '${{' "$body"

    # -- Obligation 2: every kind of disallowed deletion still FAILS. --------
    echo ""
    echo "-- disallowed deletions still fail (the vacuity guard) --"
    for probe in \
      "replaydata/agents/scenarios.json|(matrix catalog)" \
      "replaydata/orchestrators/gastown/scenarios/1-1.json|(orchestrator scenario fixture)" \
      "replaydata/agents/claudecode/regressions/8-8_frozen/assessment.json|(regression recording)" \
      "$LIVE/metadata.json|(metadata.json of live cell" \
      "$LIVE/recordings/r1/events.jsonl|(recording of live cell" \
      "$LIVE/subagents/s1.jsonl|(recording of live cell"
    do
      path=${probe%%|*}; note=${probe#*|}
      run_body "$TMP/step-body.sh" "$BASE_SHA" "$HEAD_SHA" ok "$path"
      want_status "deleting $path fails the gate" 1 "$ST" "$OUT"
      want_contains "...as a workflow error annotation" "::error::PR deletes load-bearing replaydata" "$OUT"
      want_contains "...naming it: $note" "$note" "$OUT"
    done

    # -- Obligation 3: a clean diff still passes. ---------------------------
    echo ""
    echo "-- a clean diff still passes --"
    run_body "$TMP/step-body.sh" "$BASE_SHA" "$HEAD_SHA" ok ""
    want_status "no deletions at all" 0 "$ST" "$OUT"
    want_contains "...and says so, with the count it examined" "OK: examined 0 deleted path(s)" "$OUT"
    # The anti-blindness guard: a pass obtained by never diffing is this
    # issue's own shape. The stub records every diff it was asked for.
    want_contains "...having ACTUALLY run the diff" "git diff" "$ARGV"

    # What the step asked git for, read off the invocation it actually made
    # rather than off the source. Three-dot keeps stale branches from showing
    # phantom deletions; the pathspec is what scopes the gate; --diff-filter=D
    # is what makes the output deletions at all.
    want_contains "...as a three-dot base...head diff" "$BASE_SHA...$HEAD_SHA" "$ARGV"
    want_contains "...restricted to replaydata/**" "replaydata/**" "$ARGV"
    want_contains "...filtered to deletions" "--diff-filter=D" "$ARGV"
    want_contains "...still asking for rename detection at the documented threshold" "--find-renames=50%" "$ARGV"

    # -- Obligation 4: a git that FAILS is a refusal, not a pass. -----------
    echo ""
    echo "-- a git that fails is now a REFUSAL --"
    run_body "$TMP/step-body.sh" "$BASE_SHA" "$HEAD_SHA" fail ""
    want_status "git exits 128 mid-diff" 1 "$ST" "$OUT"
    want_contains "...as a workflow error annotation, so it surfaces on the run" "::error::" "$OUT"
    want_contains "...saying the guard examined nothing" "examined NOTHING" "$OUT"
    want_contains "...and saying that is not a pass" "NOT a pass" "$OUT"
    # The heart of #1645: the failing path must not be able to print the
    # passing path's line. Both spellings are refused — the pre-fix one so a
    # revert cannot pass this arm, and the current one.
    want_absent "...and NOT the step's pass line (pre-fix spelling)" "OK: no disallowed deletions" "$OUT"
    want_absent "...and NOT the step's pass line (current spelling)" "OK: examined" "$OUT"

    # -- Obligation 5: the permitted cases still pass. ----------------------
    echo ""
    echo "-- the permitted deletions still pass --"
    for path in \
      "$ORPHAN/recordings/r1/events.jsonl" \
      "$ORPHAN/metadata.json" \
      "$LIVE/assessment.json" \
      "replaydata/README.md"
    do
      run_body "$TMP/step-body.sh" "$BASE_SHA" "$HEAD_SHA" ok "$path"
      want_status "deleting $path is permitted" 0 "$ST" "$OUT"
      want_contains "...and it examined the path rather than skipping the diff" "Examined 1 deleted path(s)" "$OUT"
    done

    # A `git mv`: real git reports it as R, which --diff-filter=D drops. The
    # stub models that faithfully — it reports the old path as a deletion only
    # when the invocation does NOT ask for rename detection — so this arm
    # grades the invocation, and would go red against a step that stopped
    # asking. See this file's header for what that does and does not prove.
    run_body "$TMP/step-body.sh" "$BASE_SHA" "$HEAD_SHA" rename "$LIVE/recordings/r1/events.jsonl"
    want_status "a git mv of a live cell's recording is permitted" 0 "$ST" "$OUT"
    want_contains "...counted as no deletion at all" "OK: examined 0 deleted path(s)" "$OUT"

    # -- Obligation 6: the two new refusals. -------------------------------
    echo ""
    echo "-- an unreadable base/head is a REFUSAL --"
    run_body "$TMP/step-body.sh" "" "" ok ""
    want_status "an empty pull_request context (workflow_dispatch)" 1 "$ST" "$OUT"
    want_contains "...naming the missing context" "no pull_request context" "$OUT"
    want_absent "...and NOT printing the pass line" "OK: examined" "$OUT"
    # It must refuse BEFORE diffing, or the refusal is decoration over a
    # HEAD...HEAD comparison that already happened.
    want_absent "...without having diffed anything" "git diff" "$ARGV"

    run_body "$TMP/step-body.sh" "$BASE_SHA" "$HEAD_SHA" ok "" "$BASE_SHA"
    want_status "a base commit this checkout does not have" 1 "$ST" "$OUT"
    want_contains "...naming which side is missing" "the base commit $BASE_SHA is not present" "$OUT"
    want_absent "...and NOT printing the pass line" "OK: examined" "$OUT"

    run_body "$TMP/step-body.sh" "$BASE_SHA" "$HEAD_SHA" ok "" "$HEAD_SHA"
    want_status "a head commit this checkout does not have" 1 "$ST" "$OUT"
    want_contains "...naming which side is missing" "the head commit $HEAD_SHA is not present" "$OUT"
    want_absent "...and NOT printing the pass line" "OK: examined" "$OUT"
  fi
fi

# ---------------------------------------------------------------------------
# ...and preflight's `tools` gate has to FIRE on a diff touching only this
# workflow, or under --changed (the pre-push hook's path) every assertion above
# is skipped on precisely the commit that can break it. That is #1591's,
# #1629's, #1639's and #1641's shape, each fixed by widening this same trigger;
# this is its fifth widening. The regex is EXTRACTED and matched rather than
# string-compared, so this is a behavioural assertion and not a lock on one
# spelling of an alternation.
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
    for probe in "$WF" tools/lib/replaydata-deletion-guard_test.sh; do
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
  echo "replaydata-deletion-guard_test: ALL PASS"
else
  echo "replaydata-deletion-guard_test: FAILURES" >&2
fi
exit "$rc"
