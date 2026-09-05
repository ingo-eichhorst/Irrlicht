#!/usr/bin/env bash
# classify-failure_test.sh — unit tests for classify-failure.sh. Plain bash +
# jq (no framework). Run directly or via scripts/smoke-test.sh.
#
# classify-failure.sh is invoked as a subprocess (not sourced — it calls
# `exit 0` from inside emit()), so each case builds a fake staging dir and
# checks the emitted JSON's .code field. Added alongside #1018's daemon_crashed
# branch, which was the first classification to read $STAGING/daemon.log —
# previously staged but never inspected.
#
# #1825 added the second half of this file: THE SYNC TRIPWIRE. Every arm of
# classify-failure.sh's manifest `case` is a string typed in one script and
# matched in another, with nothing but the two literals connecting them — and
# when they part, the classifier reports `unknown`, which is also what it
# reports for a failure nobody has categorized yet. Absence of a finding and
# inability to look print the same word, so the drift is invisible from either
# side. It had happened twice, undetected, for four months:
#
#   transcript_or_recording_missing — run-cell.sh was renamed to write
#     `transcript_recording_or_uuid_missing` in a511ad11 (2026-04-25 07:51) and
#     6d24cb14 (2026-04-25 14:34) added this classifier matching the pre-rename
#     spelling. The arm never matched, from the day it was written until
#     2026-08-25.
#   wall_clock_timeout — no script in this repo's history ever wrote it
#     (`git log -S wall_clock_timeout --all` returns only 6d24cb14, the
#     classifier itself), so `timeout` was an unreachable classification while
#     record/SKILL.md's retry policy keyed on it.
#
# The tripwire is therefore BIDIRECTIONAL, because those are two halves of one
# defect: a code a rig writes with no arm here is a failure reported as
# unknown, and an arm here with no writer is an arm that can never fire. Each
# direction is paired with a MUTATED FIXTURE it must go red against (AGENTS.md:
# a check a change adds has no "before the fix" to run red, so mutate the thing
# it protects and commit the mutation), and the extraction itself refuses rather
# than returning an empty list — a tripwire that graded nothing must not read
# as a tripwire that found nothing.

set -uo pipefail   # NOT -e: assertions capture non-zero return codes

DIR="$(cd "$(dirname "$0")" && pwd)"
SCRIPT="$DIR/classify-failure.sh"
SCRIPTS_DIR="$(cd "$DIR/.." && pwd)"
RUN_CELL="$SCRIPTS_DIR/run-cell.sh"
RUN_CELL_MULTI="$SCRIPTS_DIR/run-cell-multi.sh"

command -v jq >/dev/null || { echo "classify-failure_test: jq is required" >&2; exit 2; }

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

fails=0
pass() { local label="$1"; echo "  PASS: $label"; return 0; }
fail() { local label="$1" expected="$2" got="$3"; echo "  FAIL: $label — expected [$expected] got [$got]"; fails=$((fails + 1)); return 0; }
assert_code() {
  local label="$1" expected="$2" staging="$3"
  local got
  got="$(bash "$SCRIPT" "$staging" | jq -r '.code')"
  [[ "$got" == "$expected" ]] && pass "$label" || fail "$label" "$expected" "$got"
  return 0
}

echo "== daemon_crashed: panic in daemon.log =="
d="$TMP/panic"; mkdir -p "$d"
printf 'starting up\npanic: runtime error: invalid memory address\n' > "$d/daemon.log"
assert_code "panic: -> daemon_crashed" "daemon_crashed" "$d"

echo "== daemon_crashed: the runtime-abort prefix in daemon.log =="
d="$TMP/fatal"; mkdir -p "$d"
printf 'fatal error: all goroutines are asleep - deadlock!\n' > "$d/daemon.log"
assert_code "fatal error: -> daemon_crashed" "daemon_crashed" "$d"

echo "== daemon.log present but clean: falls through, not daemon_crashed =="
d="$TMP/clean"; mkdir -p "$d"
printf 'starting up\nlistening on :7837\nshutting down cleanly\n' > "$d/daemon.log"
assert_code "clean daemon.log -> unknown, not daemon_crashed" "unknown" "$d"

echo "== pre-existing branches still work (regression guard) =="
d="$TMP/auth"; mkdir -p "$d"
printf 'please log in to continue\n' > "$d/driver.log"
assert_code "auth failure -> auth_failed" "auth_failed" "$d"

d="$TMP/dirty"; mkdir -p "$d"
printf 'another irrlichd is running on port 7837\n' > "$d/precheck.log"
assert_code "daemon already running -> daemon_dirty" "daemon_dirty" "$d"

echo "== no signal at all -> unknown =="
d="$TMP/empty"; mkdir -p "$d"
assert_code "nothing staged -> unknown" "unknown" "$d"

# --- #1825: the manifest codes both rigs actually write ---------------------

assert_code_with() {   # <label> <classifier> <expected> <staging>
  local label="$1" classifier="$2" expected="$3" staging="$4" got
  got="$(bash "$classifier" "$staging" | jq -r '.code')"
  [[ "$got" == "$expected" ]] && pass "$label" || fail "$label" "$expected" "$got"
  return 0
}

manifest_dir() {   # <name> <json> → prints the staging dir
  local d="$TMP/$1"; mkdir -p "$d"; printf '%s\n' "$2" > "$d/run-manifest.json"; echo "$d"
}

echo "== the renamed code classifies (the arm that was dead for 4 months) =="
# a511ad11 renamed the WRITER; 6d24cb14 shipped the matcher with the old
# spelling seven hours later. The RED proof for this case is the mutated
# classifier further down, which is handed this exact input.
DRIFT_MANIFEST='{"error":"transcript_recording_or_uuid_missing","transcript_found":false,"recording_found":true,"uuid_resolved":false}'
DRIFT_DIR="$(manifest_dir drift "$DRIFT_MANIFEST")"
assert_code "transcript_recording_or_uuid_missing -> transcript_missing" \
  "transcript_missing" "$DRIFT_DIR"

echo "== the two teardown verdicts classify DIFFERENTLY (#1825 / AC4) =="
# The whole issue is that "it leaked" and "the check could not be made" must
# not print the same answer: one has a `tmux kill-session` attached, the other
# means nothing was established either way.
LEAK_DIR="$(manifest_dir leak \
  '{"error":"driver_tmux_session_survived","tmux_teardown":"leaked","tmux_teardown_detail":"claudecode-onboard-1787641617-95883","driver_pid":"95883"}')"
UNREAD_DIR="$(manifest_dir unread \
  '{"error":"driver_tmux_teardown_unreadable","tmux_teardown":"unreadable","tmux_teardown_detail":"tmux list-sessions exited 1: lost server"}')"
assert_code "a survived session -> driver_session_leaked" "driver_session_leaked" "$LEAK_DIR"
assert_code "an unreadable check -> driver_teardown_unverifiable" "driver_teardown_unverifiable" "$UNREAD_DIR"
LEAK_EVIDENCE="$(bash "$SCRIPT" "$LEAK_DIR" | jq -r '.evidence')"
[[ "$LEAK_EVIDENCE" == *"95883"* ]] \
  && pass "the leak's evidence names the session to kill" \
  || fail "the leak's evidence names the session to kill" "*95883*" "$LEAK_EVIDENCE"

echo "== driver.pid never written classifies distinctly from an unreadable tmux check (#1828) =="
# The whole point of the split: a pid-write failure must not read as "go look
# at tmux" (driver_teardown_unverifiable) — it gets its own code with its own
# first move (check $STAGING and whether the driver ever started).
PIDUNREC_DIR="$(manifest_dir pidunrec \
  '{"error":"driver_pid_unrecorded","tmux_teardown":"pid_unrecorded","tmux_teardown_detail":"driver.pid was never written at /staging/driver.pid — the pid wrapper exited before it could exec the driver"}')"
assert_code "driver.pid never written -> driver_pid_unrecorded" \
  "driver_pid_unrecorded" "$PIDUNREC_DIR"
PIDUNREC_SUMMARY="$(bash "$SCRIPT" "$PIDUNREC_DIR" | jq -r '.summary')"
[[ "$PIDUNREC_SUMMARY" == *"$PIDUNREC_DIR"*"writable"* ]] \
  && pass "the summary gives the first move (check that \$STAGING is writable)" \
  || fail "the summary gives the first move" "*$PIDUNREC_DIR*writable*" "$PIDUNREC_SUMMARY"
[[ "$PIDUNREC_SUMMARY" != *"tmux-teardown"* ]] \
  && pass "the summary does not send the operator to tmux" \
  || fail "the summary does not send the operator to tmux" "no tmux-teardown mention" "$PIDUNREC_SUMMARY"

echo "== run-cell-multi.sh's codes classify too (its driver logs are one dir down) =="
assert_code "daemon_socket_missing -> daemon_not_ready" "daemon_not_ready" \
  "$(manifest_dir sock '{"error":"daemon_socket_missing"}')"
assert_code "no_recording -> transcript_missing" "transcript_missing" \
  "$(manifest_dir norec '{"error":"no_recording"}')"
assert_code "replay_failed -> replay_failed" "replay_failed" \
  "$(manifest_dir replay '{"error":"replay_failed","failed_adapter":"kiro-cli"}')"

echo "== driver_failed deliberately falls THROUGH to the richer log heuristics =="
# The manifest code is the least informative thing known about the failure, so
# the arm exists (the tripwire below demands a decision) but does not emit.
d="$TMP/multi-auth"; mkdir -p "$d"
printf '{"error":"driver_failed"}\n' > "$d/run-manifest.json"
printf 'please log in to continue\n' > "$d/driver.log"
assert_code "driver_failed + an auth-looking driver.log -> auth_failed" "auth_failed" "$d"

echo "== timeout is reachable again, from the field the rigs really write =="
# wall_clock_timeout was never written by anything. contracts.sh's drive_exit
# sets EXIT_REASON=timeout and both rigs copy it into the manifest.
assert_code "run-cell.sh's .driver_exit_reason -> timeout" "timeout" \
  "$(manifest_dir to1 '{"driver_exit_reason":"timeout"}')"
assert_code "run-cell-multi.sh's .driver_exit_reasons map -> timeout" "timeout" \
  "$(manifest_dir to2 '{"error":"driver_failed","driver_exit_reasons":{"pi":"timeout","claudecode":"ok"}}')"
assert_code "a clean exit reason is NOT a timeout" "unknown" \
  "$(manifest_dir to3 '{"driver_exit_reason":"ok"}')"

# --- #1825: the writer <-> matcher sync tripwire ---------------------------
#
# Both directions are computed by EXTRACTING literals out of source text, so
# each extractor refuses loudly when it comes back empty. `refused` is the
# sentinel: a caller that treats it as a list would be treating "I could not
# look" as "there is nothing there", which is the failure this whole file is
# about.

# writer_reasons <script> — every error code <script> can put in a manifest's
# .error field, one per line. An argument it cannot follow is a refusal, not an
# omission: a call whose code is unquoted, or held in a variable with no literal
# assignment in the same file, means the tripwire's list is incomplete and it
# must say so rather than grading the codes it happened to understand.
writer_reasons() {
  local script="$1" kind tok var lits out="" n=0
  [[ -r "$script" ]] || { echo "refused: cannot read $script" >&2; return 1; }
  while IFS=$'\t' read -r kind tok; do
    if [[ "$kind" == "UNREADABLE" ]]; then
      echo "refused: $script has a write_error_manifest call whose code is not a quoted word: $tok" >&2
      return 1
    fi
    case "$tok" in
      '$'*)
        var="${tok#$}"
        var="${var#\{}"; var="${var%\}}"
        lits="$(awk -v v="$var" '
          $0 ~ "^[[:space:]]*(local[[:space:]]+)?" v "=\"[a-z0-9_]+\"[[:space:]]*$" {
            line=$0; sub(/^[^"]*"/, "", line); sub(/".*$/, "", line); print line
          }' "$script")"
        if [[ -z "$lits" ]]; then
          echo "refused: $script passes \$$var to write_error_manifest but assigns it no literal code" >&2
          return 1
        fi
        out+="$lits"$'\n'; n=$((n + 1))
        ;;
      [a-z0-9_]*) out+="$tok"$'\n'; n=$((n + 1)) ;;
      *) echo "refused: $script passes an uninterpretable code to write_error_manifest: [$tok]" >&2; return 1 ;;
    esac
  done < <(awk '
    /^[[:space:]]*#/               { next }
    /write_error_manifest\(\)/     { next }
    {
      s = $0
      while (match(s, /write_error_manifest[ \t]+"[^"]*"/)) {
        tok = substr(s, RSTART, RLENGTH)
        sub(/^write_error_manifest[ \t]+"/, "", tok)
        sub(/"$/, "", tok)
        print "ARG\t" tok
        s = substr(s, RSTART + RLENGTH)
      }
      if (match($0, /write_error_manifest[ \t]+[^"\t ]/)) print "UNREADABLE\t" $0
    }' "$script")
  # THE VACUITY GUARD. A rig with zero call sites is a rig this tripwire graded
  # nothing in — the same shape as righome/beacon_test.go:59-61 refusing an
  # empty corpus. Silence here would make every future drift green.
  if [[ "$n" -eq 0 ]]; then
    echo "refused: found no write_error_manifest call sites in $script — this tripwire graded nothing" >&2
    return 1
  fi
  printf '%s' "$out" | sort -u
  return 0
}

# classifier_arms <classifier> — every label of the manifest `case` block,
# which is the set of codes this classifier claims to understand. Same refusal
# contract as above.
classifier_arms() {
  local classifier="$1" arms
  [[ -r "$classifier" ]] || { echo "refused: cannot read $classifier" >&2; return 1; }
  arms="$(awk '
    /case "\$err_code" in/          { inblock = 1; next }
    inblock && /^[[:space:]]*esac/  { inblock = 0 }
    inblock && /^[[:space:]]*[a-z0-9_]+\)/ {
      line = $0; sub(/^[[:space:]]*/, "", line); sub(/\).*$/, "", line)
      if (line ~ /^[a-z0-9_]+$/) print line
    }' "$classifier" | sort -u)"
  if [[ -z "$arms" ]]; then
    echo "refused: found no manifest case arms in $classifier — this tripwire graded nothing" >&2
    return 1
  fi
  printf '%s\n' "$arms"
  return 0
}

# unmatched_codes <classifier> <script…> — codes a rig writes that the
# classifier has no arm for. Prints `refused` (and returns 1) if either side
# could not be read.
unmatched_codes() {
  local classifier="$1"; shift
  local arms reasons r missing=""
  arms="$(classifier_arms "$classifier")" || { echo refused; return 1; }
  for script in "$@"; do
    reasons="$(writer_reasons "$script")" || { echo refused; return 1; }
    while IFS= read -r r; do
      [[ -n "$r" ]] || continue
      printf '%s\n' "$arms" | grep -qx -- "$r" || missing="${missing:+$missing }$r"
    done <<< "$reasons"
  done
  printf '%s\n' "$missing"
  return 0
}

# dead_arms <classifier> <script…> — arms the classifier carries that no rig
# writes. This is the direction that would have caught BOTH original defects.
dead_arms() {
  local classifier="$1"; shift
  local arms all="" reasons a dead=""
  arms="$(classifier_arms "$classifier")" || { echo refused; return 1; }
  for script in "$@"; do
    reasons="$(writer_reasons "$script")" || { echo refused; return 1; }
    all+="$reasons"$'\n'
  done
  while IFS= read -r a; do
    [[ -n "$a" ]] || continue
    printf '%s\n' "$all" | grep -qx -- "$a" || dead="${dead:+$dead }$a"
  done <<< "$arms"
  printf '%s\n' "$dead"
  return 0
}

echo "== the tripwire can look at all (no vacuous green) =="
REAL_REASONS="$(writer_reasons "$RUN_CELL")"
[[ "$(printf '%s\n' "$REAL_REASONS" | grep -c .)" -ge 3 ]] \
  && pass "run-cell.sh yields its write_error_manifest codes ($(printf '%s\n' "$REAL_REASONS" | tr '\n' ' '))" \
  || fail "run-cell.sh yields its write_error_manifest codes" ">=3 codes" "$REAL_REASONS"
MULTI_REASONS="$(writer_reasons "$RUN_CELL_MULTI")"
[[ "$(printf '%s\n' "$MULTI_REASONS" | grep -c .)" -ge 4 ]] \
  && pass "run-cell-multi.sh yields its codes ($(printf '%s\n' "$MULTI_REASONS" | tr '\n' ' '))" \
  || fail "run-cell-multi.sh yields its codes" ">=4 codes" "$MULTI_REASONS"
REAL_ARMS="$(classifier_arms "$SCRIPT")"
[[ "$(printf '%s\n' "$REAL_ARMS" | grep -c .)" -ge 3 ]] \
  && pass "classify-failure.sh yields its case arms" \
  || fail "classify-failure.sh yields its case arms" ">=3 arms" "$REAL_ARMS"
# The variable-argument path is exercised by the real files, not only by a
# fixture: run-cell.sh:559 and run-cell-multi.sh pass "$TMUX_GATE_ERROR" /
# "$TMUX_MULTI_ERROR", so if the resolver stopped following those the two #1825
# codes would silently drop out of the list above.
printf '%s\n' "$REAL_REASONS" | grep -qx driver_tmux_session_survived \
  && pass "a code held in a variable is still followed (driver_tmux_session_survived)" \
  || fail "a code held in a variable is still followed" "driver_tmux_session_survived in list" "$REAL_REASONS"

echo "== every code either rig writes has an arm here =="
assert_str() { local label="$1" expected="$2" actual="$3"; [[ "$expected" == "$actual" ]] && pass "$label" || fail "$label" "$expected" "$actual"; return 0; }
assert_str "run-cell.sh: nothing unclassified" "" "$(unmatched_codes "$SCRIPT" "$RUN_CELL")"
assert_str "run-cell-multi.sh: nothing unclassified" "" "$(unmatched_codes "$SCRIPT" "$RUN_CELL_MULTI")"

echo "== and every arm here is a code some rig writes (no dead arms) =="
assert_str "no arm without a writer" "" "$(dead_arms "$SCRIPT" "$RUN_CELL" "$RUN_CELL_MULTI")"

echo "== MUTATION — a new writer code with no arm goes RED =="
MUT_NEW="$TMP/run-cell-with-an-unclassified-code.sh"
cp "$RUN_CELL" "$MUT_NEW"
printf '\nwrite_error_manifest "a_code_no_classifier_knows" "{}"\n' >> "$MUT_NEW"
assert_str "the unmatched code is named" "a_code_no_classifier_knows" \
  "$(unmatched_codes "$SCRIPT" "$MUT_NEW")"

echo "== MUTATION — the ORIGINAL drift (the pre-rename spelling) goes RED =="
# This is the arm that was dead from 2026-04-25 to 2026-08-25, reconstructed as
# a fixture instead of described in a PR body. Both directions must notice it,
# and the classifier built from it must misclassify the real manifest.
MUT_OLD="$TMP/classify-failure-with-the-2026-04-spelling.sh"
sed 's/transcript_recording_or_uuid_missing)/transcript_or_recording_missing)  /' \
  "$SCRIPT" > "$MUT_OLD"
assert_str "the mutant really differs from the original" "differs" \
  "$(cmp -s "$SCRIPT" "$MUT_OLD" && echo same || echo differs)"
assert_code_with "  …and it misclassifies the real manifest as unknown" \
  "$MUT_OLD" "unknown" "$DRIFT_DIR"
assert_str "  …the dead-arm direction names it" "transcript_or_recording_missing" \
  "$(dead_arms "$MUT_OLD" "$RUN_CELL" "$RUN_CELL_MULTI")"
assert_str "  …the unmatched-code direction names it too" "transcript_recording_or_uuid_missing" \
  "$(unmatched_codes "$MUT_OLD" "$RUN_CELL")"

echo "== MUTATION — an extractor that can no longer look REFUSES, never returns empty =="
MUT_NOCALLS="$TMP/run-cell-with-no-call-sites.sh"
grep -v 'write_error_manifest "' "$RUN_CELL" > "$MUT_NOCALLS"
assert_str "a rig with zero call sites is a refusal" "refused" \
  "$(unmatched_codes "$SCRIPT" "$MUT_NOCALLS" 2>/dev/null)"

MUT_UNQUOTED="$TMP/run-cell-with-an-unquoted-code.sh"
cp "$RUN_CELL" "$MUT_UNQUOTED"
printf '\nwrite_error_manifest $SOME_CODE "{}"\n' >> "$MUT_UNQUOTED"
assert_str "an argument that cannot be followed is a refusal" "refused" \
  "$(unmatched_codes "$SCRIPT" "$MUT_UNQUOTED" 2>/dev/null)"

MUT_UNRESOLVED="$TMP/run-cell-with-an-unresolvable-variable.sh"
cp "$RUN_CELL" "$MUT_UNRESOLVED"
printf '\nwrite_error_manifest "$NEVER_ASSIGNED_ANYWHERE" "{}"\n' >> "$MUT_UNRESOLVED"
assert_str "a variable with no literal assignment is a refusal" "refused" \
  "$(unmatched_codes "$SCRIPT" "$MUT_UNRESOLVED" 2>/dev/null)"

MUT_NOARMS="$TMP/classify-failure-with-no-arms.sh"
awk '/case "\$err_code" in/ { inblock = 1 }
     inblock && /^[[:space:]]*esac/ { inblock = 0; print; next }
     inblock { if ($0 ~ /case "\$err_code" in/) print; next }
     { print }' "$SCRIPT" > "$MUT_NOARMS"
assert_str "a classifier with zero arms is a refusal" "refused" \
  "$(unmatched_codes "$MUT_NOARMS" "$RUN_CELL" 2>/dev/null)"

echo ""
if [[ "$fails" -eq 0 ]]; then
  echo "classify-failure_test: ALL PASS"
else
  echo "classify-failure_test: $fails FAILED" >&2
  exit 1
fi
