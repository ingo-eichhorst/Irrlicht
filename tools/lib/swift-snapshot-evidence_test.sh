#!/usr/bin/env bash
# swift-snapshot-evidence_test.sh — the lock on
# .github/workflows/macos-swift.yml's "Collect the skipped suites' pixels"
# step (#1646).
#
# ---------------------------------------------------------------------------
# The defect
#
# That step opens with `set +e` — correctly, and with a long comment saying
# why: a step whose whole purpose is to run assertions that fail cannot
# inherit abort-on-first-failure (#1628/#1629). It then accumulates into `bad`
# and ends `exit "$bad"`. Every statement in it either sets `bad` or feeds
# something that does, with one exception:
#
#     cp -R platforms/macos/Tests/__Snapshots__ "$SNAPSHOT_ARTIFACTS/__References__"
#
# Its status was read by NOTHING. Under `set +e` a failed `cp` neither aborts
# the step nor sets `bad`, so the job stays green and the uploaded evidence
# artifact simply arrives without its `__References__` tree — the half a human
# needs in order to compare a failure image against what it should have looked
# like. A green check over a useless artifact.
#
# ---------------------------------------------------------------------------
# What this file achieves, and what it does not
#
# EXTRACT-AND-EXECUTE, per the family this belongs to (#1641, #1645): the real
# step body is read out of the real workflow file and RUN, under the shell
# GitHub actually gives that step — derived through tools/lib/workflow-step.sh,
# never typed. Running the block pins the property; a text scan pins one
# spelling of a guard.
#
# #1646 itself declined to build a reproduction, on the grounds that the step
# sources `swift-suite.sh` and drives a real `swift test`. Only the SECOND half
# of that is unreachable here, and it is the half that can be replaced without
# grading a different program:
#
#   the library   is the REAL one. The fixture checkout's
#                 `tools/lib/swift-suite.sh` sources the repo's own file by
#                 absolute path and overrides exactly one function, so
#                 `swift_suite_completed` and `swift_suite_ran_tests` — the two
#                 predicates the body consults — are the production ones,
#                 reading a real committed log fixture from
#                 tools/lib/testdata/swift-suite/.
#   `swift test`  is not run. `swift_suite_run` is the pty runner that drives
#                 it; the override writes a chosen log fixture, plants the
#                 artifacts a chosen scenario would have produced, and returns
#                 a chosen status. That is the whole of the substitution, and
#                 it is what makes each of the step's own outcomes reachable in
#                 a second rather than in a 20-minute macOS job.
#
# So what is graded is the step's STATUS ACCOUNTING — which statements can
# reach `bad`, and what each of them says — against the real body, the real
# shell, and the real predicates. What is NOT graded, said plainly rather than
# implied: whether a real `swift test` writes what the artifact scenarios below
# assume (that is #1615's evidence job doing its job on a runner), and whether
# the predicates judge a real log correctly (that is swift-suite_test.sh, which
# owns the committed log corpus this file borrows).
#
# ---------------------------------------------------------------------------
# The obligations, in order
#
#   1. the pre-fix hazard is re-MEASURED, not described: the old one-line `cp`
#      is emitted verbatim and run under the same derived shell with the source
#      absent, and it must still exit 0 having copied nothing. It doubles as
#      the vacuity guard — if `set +e` ever stopped swallowing that status the
#      fix would be protecting nothing and every arm below would pass for the
#      wrong reason.
#   2. a healthy run still exits 0, and the references actually LAND in the
#      artifact. Without this an arm that refuses everything is
#      indistinguishable from one that works.
#   3. nothing to copy → fail, naming the missing source.
#   4. the copy fails → fail, naming the copy.
#   5. the copy SUCCEEDS and lands nothing → fail. `cp -R` of an empty tree
#      exits 0, so no status check anywhere can see this one.
#   6. 3, 4 and 5 are told APART: each asserts the other two's wording is
#      absent. A shared non-zero is satisfied by three refusals that all fire
#      together — measured that way in #1644.
#   7. the references are copied even when the RUN is judged bad, and are not
#      counted as failure images. The evidence job exists to publish a failed
#      run; a copy that only happened on the happy path would ship nothing on
#      exactly the runs the artifact is for. The second half pins the
#      `-not -path '*/__References__/*'` exclusion the count depends on — 53
#      reference images would otherwise satisfy the "not one of the suites
#      produced a failure image" guard forever.
#   8. a harness that will not LOAD is refused by name, not misdiagnosed
#      (#1678). Until then this arm pinned the opposite: the step reported the
#      run as TRUNCATED, plus three more headlines, none of which happened.
#   9. a harness that loads and defines NOTHING is refused too, and told apart
#      from 8. `. an-empty-file` exits 0, so no status check anywhere can see
#      this one — it is to the source what obligation 5 is to the copy.
#  10. 8 and 9 are told APART, and neither borrows a verdict from the four
#      guards below them: each asserts the other's wording is absent, and both
#      assert all four run-shape headlines are absent. A shared exit 1 is
#      satisfied by refusals that all fire together — measured that way in
#      #1644, and measured again in #1678, where all four fired at once.
#
# ---------------------------------------------------------------------------
# The re-audit: which statement decides the status, and what it cannot see
#
# `exit "$bad"` decides for everything after the harness has loaded, and `bad`
# is written by exactly six guards — the 124 hang, the truncation, the zero-test
# run, the copy (three outcomes, above), the manifest count, and the
# failure-image count. The two load refusals (#1678) are the exception and exit
# directly, because they fire before `bad` exists and before anything those
# guards judge has been produced. Everything else in the block is invisible to
# the status unless it feeds one of the six. #1646 traced this and found one
# blind statement; measured rather than inherited, there were four. TWO are now
# read (the source, #1678; the copy, #1646), one is still unread but degrades
# loudly through two other guards, and the fourth is silent and stated rather
# than dismissed:
#
#   `. tools/lib/swift-suite.sh`  WAS read by nothing, and the result was LOUD
#                                 with the wrong headline: every `swift_suite_*`
#                                 call became "command not found", so FOUR
#                                 guards fired — TRUNCATED, `executed 0 tests`,
#                                 the missing manifest, and "not one of the five
#                                 suites produced a failure image … #1615 has
#                                 moved". #1646 said two; measured, it is four,
#                                 and the last of them accuses this workflow's
#                                 own suite classification. Read now, in two
#                                 checks, and refused before the run: #1678,
#                                 obligations 8-10 below.
#   `mkdir -p "$SNAPSHOT_ARTIFACTS"`
#                                 read by nothing, and covered more strongly
#                                 than #1646 claimed: with the directory
#                                 missing, `expected` comes back empty (the
#                                 `wc -l < …` REDIRECT fails, which is why that
#                                 one carries a `:-0` default and the two
#                                 `find | wc -l` counts do not — those always
#                                 print `0`), so the manifest guard AND the
#                                 failure-image guard both fire. Measured.
#   `cp -R …`                     the defect. Fixed above; obligations 1-7.
#   `… >> "$GITHUB_STEP_SUMMARY"` read by nothing and genuinely SILENT —
#                                 measured, a failing append leaves the step at
#                                 exit 0. Left alone deliberately: the same text
#                                 is echoed to stdout on the line above, so the
#                                 blast radius is a missing line in the run
#                                 summary and no information is lost. Stated
#                                 here so the next reader does not have to
#                                 re-derive it.
#
# The two `if: always()` upload steps are outside this block and cannot be
# reached by `bad` at all; both declare `if-no-files-found: error`, so an
# artifact path that stopped matching fails the job rather than uploading
# nothing quietly. Read, not assumed.
set -uo pipefail

REPO_ROOT=$(git rev-parse --show-toplevel)
cd "$REPO_ROOT" || { echo "FAIL: swift-snapshot-evidence_test — cannot cd to $REPO_ROOT" >&2; exit 1; }

# A missing tool is a hard failure, not a skip: exiting 0 here would read as a
# PASS to shell-lib-suite.sh, so the gate would go green having asserted
# nothing — the failure mode this whole issue family is made of.
need() { command -v "$1" >/dev/null 2>&1 || { echo "FAIL: swift-snapshot-evidence_test — $1 not found" >&2; exit 1; }; }
need git
need mktemp
need bash
need cp
need find
need grep
need id

WF=.github/workflows/macos-swift.yml
STEP="Collect the skipped suites' pixels"
REAL_LIB="$REPO_ROOT/tools/lib/swift-suite.sh"
LOGDATA="$REPO_ROOT/tools/lib/testdata/swift-suite"

# shellcheck source=workflow-step.sh
. tools/lib/workflow-step.sh

TMP=$(mktemp -d -t swift-snapshot-evidence) || exit 1
# One arm deliberately makes a directory unwritable, so the cleanup has to be
# able to get back in. `u+rwX` and not `777`: the point is to undo the arm, not
# to widen anything.
trap 'chmod -R u+rwX "$TMP" 2>/dev/null; rm -rf "$TMP"' EXIT

rc=0
pass() { echo "  PASS: $1"; return 0; }
fail() { echo "  FAIL: $1 — expected [$2] got [$3]" >&2; rc=1; return 0; }
flat() { echo "$1" | tr '\n' ' ' | cut -c1-400; }

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
# Preconditions. Each of these is something that, if it quietly stopped being
# true, would leave every arm below grading something other than what it says.

# Running as root defeats the mode bits obligation 4 relies on, and a `cp` that
# succeeded there would report a MISSING arm instead of a FAILED one — a red
# for the wrong reason at best. Refuse rather than skip.
if [[ "$(id -u)" -eq 0 ]]; then
  echo "FAIL: swift-snapshot-evidence_test — running as root; the unwritable-destination arm cannot be driven, and a skip would read as a pass" >&2
  exit 1
fi

for f in clean hung zero-tests; do
  [[ -s "$LOGDATA/$f.log" ]] || { echo "FAIL: swift-snapshot-evidence_test — log fixture $LOGDATA/$f.log missing or empty" >&2; exit 1; }
done
[[ -f "$REAL_LIB" ]] || { echo "FAIL: swift-snapshot-evidence_test — $REAL_LIB not found" >&2; exit 1; }

# ---------------------------------------------------------------------------
# The invocation and the body, DERIVED from the workflow (#1650).
#
# macos-swift.yml is the ONE workflow in this repo that declares `shell: bash`
# on a step, so this step's invocation genuinely differs from every other
# harnessed step's — `bash --noprofile --norc -e -o pipefail` against the
# `bash -e` the others get. Typing either would be a coin flip that reads as
# fact; workflow-step.sh REFUSES rather than defaulting, which is why both of
# these are hard exits and not fallbacks.
if ! STEP_SHELL=$(workflow_step_shell "$WF" "$STEP"); then
  echo "FAIL: swift-snapshot-evidence_test — could not derive the shell $WF gives '$STEP' (refusal above); nothing below would have graded the real program" >&2
  exit 1
fi
read -r -a STEP_ARGV <<<"$STEP_SHELL"
if ! STEP_BODY=$(workflow_step_body "$WF" "$STEP"); then
  echo "FAIL: swift-snapshot-evidence_test — could not extract the body of '$STEP' from $WF (refusal above)" >&2
  exit 1
fi
printf '%s\n' "$STEP_BODY" >"$TMP/step.sh"
echo "== $WF :: '$STEP' runs under \`$STEP_SHELL\` (derived) =="

# The substitution has to still be the substitution. If the body stopped
# calling one of these — or the library stopped defining it — the fixture
# library below would be overriding nothing, or shadowing something real, and
# every arm would grade a different program while still going green.
for fn in swift_suite_run swift_suite_completed swift_suite_ran_tests; do
  grep -q "$fn" "$TMP/step.sh" \
    || { echo "FAIL: swift-snapshot-evidence_test — the step body no longer calls $fn; the fixture library is grading something else" >&2; exit 1; }
  grep -qE "^$fn\(\)" "$REAL_LIB" \
    || { echo "FAIL: swift-snapshot-evidence_test — $REAL_LIB no longer defines $fn" >&2; exit 1; }
done

# ---------------------------------------------------------------------------
# The fixture checkout.
#
# The body runs `cd platforms/macos` and sources `tools/lib/swift-suite.sh`
# relative to its cwd, so every arm executes against a throwaway tree under
# $TMP. The repo's real platforms/macos/Tests/__Snapshots__ is READ by nothing
# here and written by nothing here; the reference images below are two empty
# files, because what is being graded is the copy's accounting, not its bytes.
CHECKOUT="$TMP/checkout"

# build_checkout <refs-mode>   full | empty | absent
#
# `platforms/macos` itself always exists — the body cds into it, and a cd that
# failed would give an arm a status unrelated to its obligation.
build_checkout() {
  rm -rf "$CHECKOUT"
  mkdir -p "$CHECKOUT/tools/lib" "$CHECKOUT/platforms/macos/Tests"
  case "$1" in
    full)
      mkdir -p "$CHECKOUT/platforms/macos/Tests/__Snapshots__/SessionRowSnapshotTests"
      : >"$CHECKOUT/platforms/macos/Tests/__Snapshots__/SessionRowSnapshotTests/testRow.1.png"
      : >"$CHECKOUT/platforms/macos/Tests/__Snapshots__/SessionRowSnapshotTests/testRow.2.png" ;;
    empty)
      # Present but carrying no images — the state in which `cp -R` succeeds
      # and ships nothing.
      mkdir -p "$CHECKOUT/platforms/macos/Tests/__Snapshots__" ;;
    absent) : ;;
    *) echo "FAIL: swift-snapshot-evidence_test — unmodelled refs mode: $1" >&2; exit 1 ;;
  esac

  # The fixture library: the REAL one, with the pty runner replaced. Sourcing
  # by absolute path is what keeps `swift_suite_completed` and
  # `swift_suite_ran_tests` production code reading a production log fixture.
  cat >"$CHECKOUT/tools/lib/swift-suite.sh" <<'STUBLIB'
. "$SSE_REAL_LIB"
swift_suite_run() {
  local log="$1"
  cp "$SSE_LOG_SRC" "$log" || return 1
  if [ -n "${SSE_ARTIFACTS:-}" ]; then
    bash "$SSE_ARTIFACTS" || return 1
  fi
  return "${SSE_RC:-1}"
}
STUBLIB
  return 0
}

# The artifact scenarios. Each is a script run (by the fixture library) at the
# moment the real suite would have finished writing into $SNAPSHOT_ARTIFACTS,
# with that variable exported by the body exactly as in production.
#
# `manifest.txt` carries one line per primitive, which is what the step derives
# `expected` from — so a scenario cannot state a count the images do not back.
mk_artifacts() { # <file> <failure-images> [extra shell]
  cat >"$1" <<ART
set -e
mkdir -p "\$SNAPSHOT_ARTIFACTS/RasterPrimitiveEvidence"
: >"\$SNAPSHOT_ARTIFACTS/RasterPrimitiveEvidence/flat.png"
: >"\$SNAPSHOT_ARTIFACTS/RasterPrimitiveEvidence/stroke.png"
: >"\$SNAPSHOT_ARTIFACTS/RasterPrimitiveEvidence/text.png"
: >"\$SNAPSHOT_ARTIFACTS/RasterPrimitiveEvidence/symbol.png"
printf 'flat\nstroke\ntext\nsymbol\n' >"\$SNAPSHOT_ARTIFACTS/RasterPrimitiveEvidence/manifest.txt"
ART
  if [[ "$2" -gt 0 ]]; then
    cat >>"$1" <<ART
mkdir -p "\$SNAPSHOT_ARTIFACTS/SessionRowSnapshotTests"
: >"\$SNAPSHOT_ARTIFACTS/SessionRowSnapshotTests/testRow.1.png"
ART
  fi
  if [[ -n "${3:-}" ]]; then printf '%s\n' "$3" >>"$1"; fi
  return 0
}

ARM=0
# run_step <script> <refs-mode> <log-fixture> <rc> [artifacts-script]
# -> sets OUT / ST / RT (this arm's RUNNER_TEMP)
run_step() {
  ARM=$((ARM + 1))
  RT="$TMP/rt.$ARM"
  mkdir -p "$RT"
  # No `set +e` around this. THIS file runs under `set -uo pipefail` and
  # deliberately not `-e`, so a non-zero inner status — the expected outcome of
  # most arms — is data, not an abort. Toggling errexit here would leave it on
  # for the rest of the file, which is the option-you-cannot-see family the
  # sibling issues (#1633, #1635) are made of.
  OUT=$(cd "$CHECKOUT" && env \
          RUNNER_TEMP="$RT" \
          GITHUB_STEP_SUMMARY="$RT/summary.md" \
          SSE_REAL_LIB="$REAL_LIB" \
          SSE_LOG_SRC="$LOGDATA/$3.log" \
          SSE_RC="$4" \
          SSE_ARTIFACTS="${5:-}" \
          "${STEP_ARGV[@]}" "$1" 2>&1)
  ST=$?
  return 0
}

MISSING='there is no platforms/macos/Tests/__Snapshots__ to copy'
CANNOT='could not copy platforms/macos/Tests/__Snapshots__'
LANDED_NOTHING='reference copy reported success but'
COPIED='reference image(s) into the artifact'

# The two load refusals (#1678), and the four run-shape headlines they must not
# borrow a verdict from. Before #1678 a harness that would not load printed all
# four of these and none of its own — the whole of obligations 8-10.
NOLOAD='could not load the test harness tools/lib/swift-suite.sh'
UNUSABLE='was read but defines no'
TRUNCATED='TRUNCATED'
ZERO_TESTS='executed 0 tests'
NO_MANIFEST='wrote no manifest'
NO_FAILURES='not one of the five suites produced a failure image'

# ---------------------------------------------------------------------------
# Obligation 1 — the pre-#1646 hazard, re-measured on every run.
#
# The old statement verbatim, in the two lines of context that decide its fate:
# the block's own `set +e`, and the `exit "$bad"` that is the only thing
# deciding the step's status. Committed here rather than quoted in an issue,
# per AGENTS.md: a number — or a behaviour — which documents something but is
# not produced by it drifts silently.
echo ""
echo "== the pre-#1646 bare \`cp\` (the defect, re-measured) =="
build_checkout absent
cat >"$TMP/predecessor-cp.sh" <<'OLD'
set +e
set -uo pipefail
bad=0
export SNAPSHOT_ARTIFACTS="$RUNNER_TEMP/snapshot-artifacts"
mkdir -p "$SNAPSHOT_ARTIFACTS"
cp -R platforms/macos/Tests/__Snapshots__ "$SNAPSHOT_ARTIFACTS/__References__"
exit "$bad"
OLD
run_step "$TMP/predecessor-cp.sh" absent clean 0 ""
if [[ "$ST" -eq 0 ]]; then
  pass "the old bare \`cp\` still exits 0 when the source is absent — the hazard is real"
else
  fail "the pre-fix copy exits 0 under \`set +e\` when it fails (the hazard this pins)" \
       "exit 0" "exit $ST — the hazard is GONE, so re-derive the fix rather than trusting it :: $(flat "$OUT")"
fi
if [[ -e "$RT/snapshot-artifacts/__References__" ]]; then
  fail "...having copied nothing" "no __References__ in the artifact" "the directory exists — the arm did not reproduce the defect"
else
  pass "...having produced no __References__ tree in the artifact it uploads"
fi

# ---------------------------------------------------------------------------
# Obligation 2 — the vacuity guard: a healthy run still passes, and the
# references demonstrably land where the upload step ships them from.
echo ""
echo "== the real step, healthy run =="
build_checkout full
mk_artifacts "$TMP/art-healthy.sh" 1
run_step "$TMP/step.sh" full clean 0 "$TMP/art-healthy.sh"
want_status "a healthy evidence run passes" 0 "$ST" "$OUT"
want_contains "...and says how many references it copied" "$COPIED" "$OUT"
if [[ -f "$RT/snapshot-artifacts/__References__/SessionRowSnapshotTests/testRow.1.png" ]]; then
  pass "...with the reference tree actually present under \$RUNNER_TEMP/snapshot-artifacts, which is what the upload step ships"
else
  fail "the references land in the uploaded artifact" "__References__/SessionRowSnapshotTests/testRow.1.png under $RT/snapshot-artifacts" \
       "$(find "$RT/snapshot-artifacts" 2>/dev/null | tr '\n' ' ')"
fi
want_absent "...and reports none of the three copy refusals" "::error::" "$OUT"

# ---------------------------------------------------------------------------
# Obligation 3 — nothing to copy.
echo ""
echo "== nothing to copy =="
build_checkout absent
mk_artifacts "$TMP/art-absent.sh" 1
run_step "$TMP/step.sh" absent clean 0 "$TMP/art-absent.sh"
want_status "a missing reference tree FAILS the step" 1 "$ST" "$OUT"
want_contains "...naming the source that is not there" "$MISSING" "$OUT"
want_absent "...and not the 'could not copy' diagnosis (obligation 6)" "$CANNOT" "$OUT"
want_absent "...and not the 'landed nothing' diagnosis (obligation 6)" "$LANDED_NOTHING" "$OUT"
# The other guards must stay quiet, or this arm's non-zero would be someone
# else's: a shared exit status is satisfied by every refusal firing at once.
want_absent "...with the run itself still judged healthy" "$TRUNCATED" "$OUT"

# ---------------------------------------------------------------------------
# Obligation 4 — the copy fails.
#
# The destination is made unwritable at the moment the suite finishes, which is
# after the step's own `mkdir -p` and before its copy — the only window in
# which `cp` can fail with the source present.
echo ""
echo "== the copy fails =="
build_checkout full
mk_artifacts "$TMP/art-locked.sh" 1 'chmod 500 "$SNAPSHOT_ARTIFACTS"'
run_step "$TMP/step.sh" full clean 0 "$TMP/art-locked.sh"
chmod -R u+rwX "$RT" 2>/dev/null
want_status "an unwritable destination FAILS the step" 1 "$ST" "$OUT"
want_contains "...naming the copy that could not be made" "$CANNOT" "$OUT"
want_absent "...and not the 'nothing to copy' diagnosis (obligation 6)" "$MISSING" "$OUT"
want_absent "...and not the 'landed nothing' diagnosis (obligation 6)" "$LANDED_NOTHING" "$OUT"

# ---------------------------------------------------------------------------
# Obligation 5 — the copy succeeds and ships nothing.
#
# The one outcome no exit status can see: `cp -R` of an empty tree returns 0,
# so the artifact arrives with a `__References__` directory and no references
# in it, which reads exactly like health.
echo ""
echo "== the copy succeeds and lands nothing =="
build_checkout empty
mk_artifacts "$TMP/art-emptyrefs.sh" 1
run_step "$TMP/step.sh" empty clean 0 "$TMP/art-emptyrefs.sh"
want_status "an empty reference tree FAILS the step even though cp succeeded" 1 "$ST" "$OUT"
want_contains "...naming the copy that reported success and landed nothing" "$LANDED_NOTHING" "$OUT"
want_absent "...and not the 'nothing to copy' diagnosis (obligation 6)" "$MISSING" "$OUT"
want_absent "...and not the 'could not copy' diagnosis (obligation 6)" "$CANNOT" "$OUT"
# ...and the premise of the arm: cp really did succeed.
if [[ -d "$RT/snapshot-artifacts/__References__" ]]; then
  pass "...with the copy having demonstrably succeeded (the directory is there)"
else
  fail "the empty-copy arm's premise" "an empty __References__ directory in the artifact" "it is absent — cp failed, so this arm graded obligation 4"
fi

# ---------------------------------------------------------------------------
# Obligation 7 — the references travel with a FAILED run, and are not counted
# as failure images.
#
# This job exists to publish a run that fails; a copy that only happened on the
# happy path would ship nothing on exactly the runs the artifact is for. The
# second half pins the count's `__References__` exclusion: the real tree holds
# 53 images, so without that exclusion the "not one of the suites produced a
# failure image" guard could never fire again.
echo ""
echo "== a truncated run still publishes its references, and they are not miscounted =="
build_checkout full
mk_artifacts "$TMP/art-nofailures.sh" 0
run_step "$TMP/step.sh" full hung 1 "$TMP/art-nofailures.sh"
want_status "a truncated run fails the step" 1 "$ST" "$OUT"
want_contains "...diagnosed as truncated" "$TRUNCATED" "$OUT"
# The other direction of obligation 10, and the vacuity guard for the whole of
# #1678: a load refusal that fired unconditionally would swallow this arm, and a
# real truncation must still be reported as one.
want_absent "...and NOT as a harness that could not be loaded (#1678)" "$NOLOAD" "$OUT"
want_absent "...nor as a harness that loaded and is unusable (#1678)" "$UNUSABLE" "$OUT"
want_contains "...with the references copied anyway" "$COPIED" "$OUT"
want_absent "...and no copy refusal" "$CANNOT" "$OUT"
want_contains "...and the copied references NOT counted as failure images" \
  "not one of the five suites produced a failure image" "$OUT"
want_contains "...which the count line says out loud" "collected 0 failure image(s)" "$OUT"

# ---------------------------------------------------------------------------
# Obligations 8 and 10 — the harness cannot be loaded at all.
#
# `. tools/lib/swift-suite.sh` was the other statement in the block whose status
# reached nothing. It was never silent: with the library gone, every
# `swift_suite_*` call becomes "command not found" and the step exits 1. What it
# got WRONG was the diagnosis, and by more than #1646 recorded — measured, FOUR
# headlines fire, the first pointing a reader at XCTest's stall detector
# (#1523) and the last accusing this workflow's own suite classification of
# being stale. This arm asserted the first of those until #1678, which is a test
# pinning incorrect behaviour: the four `want_absent`s below are the correction,
# and each of them is the arm that would have gone red then.
echo ""
echo "== a harness that does not load is refused BY NAME (#1678) =="
build_checkout full
rm -f "$CHECKOUT/tools/lib/swift-suite.sh"
mk_artifacts "$TMP/art-nolib.sh" 1
run_step "$TMP/step.sh" full clean 0 "$TMP/art-nolib.sh"
want_status "a missing swift-suite.sh fails the step rather than passing quietly" 1 "$ST" "$OUT"
want_contains "...naming the harness it could not load" "$NOLOAD" "$OUT"
want_absent "...and NOT diagnosed as a truncated test bundle (#1678)" "$TRUNCATED" "$OUT"
want_absent "...nor as a run that executed 0 tests" "$ZERO_TESTS" "$OUT"
want_absent "...nor as a fixture that wrote no manifest" "$NO_MANIFEST" "$OUT"
want_absent "...nor as five suites that have started passing on a runner" "$NO_FAILURES" "$OUT"
want_absent "...and not the other load refusal's wording (obligation 10)" "$UNUSABLE" "$OUT"

# ---------------------------------------------------------------------------
# Obligations 9 and 10 — the harness loads and is unusable.
#
# The outcome no status check can see, and the reason the fix is two checks
# rather than one: `. an-empty-file` exits 0. A truncated, emptied or
# half-written library therefore satisfies the source's own status while
# defining none of the predicates, and every headline above comes back. This is
# to the source exactly what obligation 5 is to the copy.
echo ""
echo "== a harness that loads and defines nothing is refused too (#1678) =="
build_checkout full
: >"$CHECKOUT/tools/lib/swift-suite.sh"
mk_artifacts "$TMP/art-emptylib.sh" 1
run_step "$TMP/step.sh" full clean 0 "$TMP/art-emptylib.sh"
want_status "a swift-suite.sh that defines nothing fails the step" 1 "$ST" "$OUT"
want_contains "...naming the predicate it does not define" "$UNUSABLE" "$OUT"
want_absent "...and not the 'could not load' diagnosis (obligation 10)" "$NOLOAD" "$OUT"
want_absent "...and NOT diagnosed as a truncated test bundle" "$TRUNCATED" "$OUT"
want_absent "...nor as a run that executed 0 tests" "$ZERO_TESTS" "$OUT"
want_absent "...nor as a fixture that wrote no manifest" "$NO_MANIFEST" "$OUT"
want_absent "...nor as five suites that have started passing on a runner" "$NO_FAILURES" "$OUT"
# ...and the premise of the arm: sourcing an empty file really does succeed, so
# the status check alone could not have caught this one.
if bash -c '. "$1"' _ "$CHECKOUT/tools/lib/swift-suite.sh"; then
  pass "...with sourcing the empty library having demonstrably SUCCEEDED (exit 0), which is why the second check exists"
else
  fail "the unusable-library arm's premise" "sourcing an empty file to exit 0" \
       "it exited non-zero — this arm graded obligation 8"
fi

echo ""
if [[ "$rc" -eq 0 ]]; then
  echo "swift-snapshot-evidence_test: OK"
else
  echo "swift-snapshot-evidence_test: FAILED" >&2
fi
exit "$rc"
