#!/usr/bin/env bash
# test-mac-script_test.sh — the LOCK TEST for the gates in
# .claude/skills/ir:test-mac/test-mac.sh (#1855).
#
# WHAT THIS IS. #1855 turned nine fenced bash blocks in that skill's SKILL.md
# into one script. Six gates were MOVED across in that extraction and four were
# added; each has an incident behind it and several fail SILENTLY when broken,
# which is exactly why an extraction is where they get lost. This file drives
# both of the skill's scripts end to end and asserts each gate still holds.
# tools/lib/test-mac-script-mutations_test.sh then breaks each one in turn and
# requires THIS file to go red — a green that was never red is a claim, not
# evidence (AGENTS.md, Testing).
#
# GATES ARE NOT THE WHOLE JOB. A gate says "it refuses correctly"; the CALL-SITE
# cases below say "it does the right thing when it proceeds". That half exists
# because a mutation battery over the first draft found eleven survivors and
# every one was a call site — --record dropped from the daemon launch, the
# replace-mode port changed, separate mode's whole bundle assembly never
# executed. A refactor moves risk to call sites, and this ticket is a refactor.
#
# SAFETY. The scripts under test kill processes and overwrite (test-mac.sh) or
# delete and replace (restore-prod.sh) /Applications/Irrlicht.app. Four things
# keep them off this machine, and every one is load-bearing:
#   1. IRRLICHT_TESTMAC_PROD_APP / _DEV_APP / _MAIN_REPO / _REPO_ROOT /
#      _LOG_DIR / _PLISTBUDDY redirect every path it touches into a mktemp -d.
#   2. HOME is that temp dir too, so the replace-mode socket path under
#      ~/.local/share/irrlicht/ resolves inside the fixture.
#   3. PATH is fronted by stubs for pkill/pgrep/lsof/curl/open/nohup/codesign/
#      security/install_name_tool/go/swift, so nothing can reach a real
#      process, port or signing identity.
#   4. `env -i` means nothing leaks in from this shell.
# A case that skips any of the four can destroy the machine it runs on. Build
# every environment through new_env().
#
# `sleep` is stubbed too — not for safety but so the script's real polling
# loops (5 iterations for the app-exit wait, 8 for the daemon readiness gate)
# run at full fidelity in zero time. The loops are the thing under test; their
# wall-clock cost is not.
#
# CASE SELECTION. With no case named every case runs, which is what the `tools`
# gate does. Naming one (argv, or $TESTMAC_CASE) runs only that one — which is
# how the mutations fixture keeps its cost sane. Re-running every case for each
# mutation took 194s for that file and pushed the whole `tools` gate from 116s
# to 327s, past what the pre-push hook's 540s budget can absorb alongside every
# other gate. Figures measured with:
#     S=$(date +%s); tools/preflight.sh --only tools >/dev/null 2>&1; echo $(( $(date +%s) - S ))
# re-run after any change here rather than trusted from this comment. A NAME
# THAT MATCHES NOTHING IS A REFUSAL (exit 2), never a quiet zero-case pass: a
# typo in a fixture row must not produce the same output as a case that ran.
#
# Convention follows tools/lib/install-uninstall_test.sh and
# tools/lib/agents-md-lint_test.sh: plain bash, `set -uo pipefail` (never -e —
# a non-zero status from the subject is DATA here, most cases expect one), a
# `fails` counter, and "ALL PASS" / "N FAILED" at the end. FAIL lines start at
# column 0 because tools/lib/mutation-assert.sh greps `^FAIL:` to decide
# whether a mutation went red.
set -uo pipefail

NAME=test-mac-script_test
REPO_ROOT=$(git rev-parse --show-toplevel) || { echo "FAIL: $NAME — not inside a git repo" >&2; exit 2; }
cd "$REPO_ROOT" || { echo "FAIL: $NAME — cannot cd to $REPO_ROOT" >&2; exit 2; }

SCRIPT=".claude/skills/ir:test-mac/test-mac.sh"
# Positional for a human ("run just this one"), $TESTMAC_CASE for the mutations
# fixture — tools/lib/mutation-assert.sh runs the lock test as a bare
# `bash <path>` and has no way to append an argument, so the env spelling is
# the one it can actually reach. Positional wins if both are given.
CASE_FILTER="${1:-${TESTMAC_CASE:-}}"

# A missing tool is a hard REFUSAL (exit 2), never a skip: exiting 0 here would
# read as a PASS to shell-lib-suite.sh, so the gate would go green having
# asserted nothing.
need() { command -v "$1" >/dev/null 2>&1 || { echo "FAIL: $NAME — $1 not found, so nothing below could be checked" >&2; exit 2; }; }
need bash; need mktemp; need grep; need sed; need touch; need find; need chmod

if [[ ! -x "$REPO_ROOT/$SCRIPT" ]]; then
  echo "FAIL: $NAME — $SCRIPT is missing or not executable; the whole corpus is unreachable" >&2
  exit 2
fi

# The isolation seams are what keep every case below off the real machine. If
# either script stops honouring them, this file would silently start driving
# the real /Applications/Irrlicht.app — so refuse to run rather than find out.
#
# This is a cheap TEXT check and it is deliberately not the only one: a seam
# that survives only inside a comment would pass it. The `seams` case below is
# the behavioural one — it points a seam at a fixture path and requires the
# script's own error to name THAT path. Both exist because this check runs
# before anything else and must be able to stop the file dead, while the case
# is what actually proves the redirection works.
for seam in IRRLICHT_TESTMAC_REPO_ROOT IRRLICHT_TESTMAC_MAIN_REPO IRRLICHT_TESTMAC_PROD_APP \
            IRRLICHT_TESTMAC_DEV_APP IRRLICHT_TESTMAC_LOG_DIR IRRLICHT_TESTMAC_PLISTBUDDY; do
  if ! grep -q "$seam" "$REPO_ROOT/$SCRIPT"; then
    echo "FAIL: $NAME — REFUSING TO RUN: $SCRIPT no longer honours \$$seam." >&2
    echo "      Without it this test drives the real production app and daemon." >&2
    exit 2
  fi
done
for seam in IRRLICHT_TESTMAC_MAIN_REPO IRRLICHT_TESTMAC_PROD_APP IRRLICHT_TESTMAC_PORT; do
  if ! grep -q "$seam" "$REPO_ROOT/.claude/skills/ir:test-mac/restore-prod.sh"; then
    echo "FAIL: $NAME — REFUSING TO RUN: restore-prod.sh no longer honours \$$seam." >&2
    echo "      Without it the restore cases below delete the real /Applications/Irrlicht.app." >&2
    exit 2
  fi
done

fails=0
pass() { echo "  PASS: $1"; }
fail() { echo "FAIL: $1"; fails=$((fails + 1)); }

WORK="$(mktemp -d "${TMPDIR:-/tmp}/test-mac-script.XXXXXX")" || exit 2
trap 'rm -rf "$WORK"' EXIT

# new_env — build one hermetic fixture. Echoes its root.
#
# The root comes from `mktemp -d` rather than an incrementing counter because
# every caller is `R=$(new_env)` — a COMMAND SUBSTITUTION, so a counter
# incremented here lives in a subshell and never reaches the next call. Two
# cases would then share one directory, and a backup dir created by one would
# silently satisfy the next one's refusal gate. (Measured: that is exactly what
# the counter version did, and the backup-refusal case caught it.)
#
# The bundle's executable and Sparkle.framework carry a "PRODUCTION" sentinel:
# every abort case below asserts the sentinel SURVIVED, which is what proves
# the gate fired BEFORE the destructive write rather than merely printing
# something on the way past it.
new_env() {
  local root
  # A failed mktemp must not yield an empty root: `IRRLICHT_TESTMAC_PROD_APP=`
  # is an EMPTY override, so the script falls back to its real default and the
  # case starts driving /Applications/Irrlicht.app. No caller checks new_env's
  # status (they are all `R=$(new_env)`), so the refusal has to be fatal here.
  if ! root="$(mktemp -d "$WORK/env.XXXXXX")" || [[ -z "$root" || ! -d "$root" ]]; then
    echo "FAIL: $NAME — could not create a fixture root under $WORK; refusing to run a case that would fall back to the real machine" >&2
    exit 2
  fi
  mkdir -p "$root/home" "$root/bin" "$root/logs"

  # --- the worktree the script builds from ---
  mkdir -p "$root/repo/core/cmd/irrlichd"
  mkdir -p "$root/repo/platforms/macos/Irrlicht/Resources"
  mkdir -p "$root/repo/platforms/macos/.build/arm64-apple-macosx/debug/Sparkle.framework"
  echo 'import Foundation' > "$root/repo/platforms/macos/Irrlicht/App.swift"
  echo 'icns'              > "$root/repo/platforms/macos/Irrlicht/Resources/AppIcon.icns"
  echo '<plist/>'          > "$root/repo/platforms/macos/Irrlicht/Resources/Irrlicht-dev.entitlements"
  echo 'FRESH-BUILD'       > "$root/repo/platforms/macos/.build/arm64-apple-macosx/debug/Irrlicht"
  echo 'sparkle'           > "$root/repo/platforms/macos/.build/arm64-apple-macosx/debug/Sparkle.framework/marker"
  chmod +x "$root/repo/platforms/macos/.build/arm64-apple-macosx/debug/Irrlicht"
  # Explicit timestamps, not sleeps: sources OLDER than the build product, so
  # the freshness gate passes by default and a case that wants it to fire says
  # so by touching one source forward.
  touch -t 202601010101 "$root/repo/platforms/macos/Irrlicht/App.swift"
  touch -t 202601020101 "$root/repo/platforms/macos/.build/arm64-apple-macosx/debug/Irrlicht"

  # --- the stable main checkout (daemon binary + production backup live here) ---
  mkdir -p "$root/main/core/bin" "$root/main/.build"

  # The replace-mode socket lives under $HOME; pre-create it so the cleanup
  # step has something to actually remove and its absence afterwards means
  # something.
  mkdir -p "$root/home/.local/share/irrlicht"
  echo 'stale-socket' > "$root/home/.local/share/irrlicht/irrlichd.sock"

  # --- the "installed" production bundle ---
  local app="$root/Applications/Irrlicht.app"
  mkdir -p "$app/Contents/MacOS" "$app/Contents/Frameworks/Sparkle.framework" "$app/Contents/Resources"
  echo 'PRODUCTION' > "$app/Contents/MacOS/Irrlicht"
  echo 'PRODUCTION' > "$app/Contents/Frameworks/Sparkle.framework/marker"
  echo 'PRODUCTION' > "$app/Contents/Resources/AppIcon.icns"
  echo '<plist/>'   > "$app/Contents/Info.plist"

  # --- stubs ---
  local b="$root/bin"
  # Recording stubs: an assertion can prove a call ACTUALLY HAPPENED (and in
  # what order) rather than that the script merely exited without complaining.
  local s
  for s in pkill install_name_tool; do
    printf '#!/bin/sh\nprintf "%%s %%s\\n" "%s" "$*" >>"$STUB_LOG"\nexit 0\n' "$s" > "$b/$s"
  done
  # open: STUB_OPEN_RC=1 stands in for any failure AFTER the bundle has been
  # overwritten, which is what the teardown-hint case needs.
  printf '#!/bin/sh\nprintf "open %%s\\n" "$*" >>"$STUB_LOG"\nexit "${STUB_OPEN_RC:-0}"\n' > "$b/open"
  # nohup records the ENVIRONMENT it was handed, not just its argv. Everything
  # that decides what the daemon actually is — `--record`, the bind address, the
  # state dir, the permission mode — is passed that way, so an assertion that
  # cannot see it cannot tell a correct launch from a silently wrong one. Every
  # one of those was a surviving mutation before this (#1855 review F3).
  cat > "$b/nohup" <<'STUB'
#!/bin/sh
printf 'nohup BIND=%s IRRHOME=%s PERM=%s ARGV=%s\n' \
  "${IRRLICHT_BIND_ADDR:-<unset>}" "${IRRLICHT_HOME:-<unset>}" \
  "${IRRLICHT_PERMISSION_MODE:-<unset>}" "$*" >>"$STUB_LOG"
exit 0
STUB
  # pgrep: exit 1 = "no such process" (the app died). STUB_PGREP_RC=0 makes it
  # immortal, which is what the app-exit gate is for.
  printf '#!/bin/sh\nprintf "pgrep %%s\\n" "$*" >>"$STUB_LOG"\nexit "${STUB_PGREP_RC:-1}"\n' > "$b/pgrep"
  # lsof: nothing listening, nothing to kill.
  printf '#!/bin/sh\nexit 1\n' > "$b/lsof"
  # curl: STUB_CURL_RC=1 = the daemon never answers /state.
  printf '#!/bin/sh\nprintf "curl %%s\\n" "$*" >>"$STUB_LOG"\nexit "${STUB_CURL_RC:-0}"\n' > "$b/curl"
  # go / swift: compile nothing. STUB_SWIFT_RC=1 = a failed swift build.
  printf '#!/bin/sh\nprintf "go %%s\\n" "$*" >>"$STUB_LOG"\nexit 0\n' > "$b/go"
  printf '#!/bin/sh\nprintf "swift %%s\\n" "$*" >>"$STUB_LOG"\necho "stub swift build"\nexit "${STUB_SWIFT_RC:-0}"\n' > "$b/swift"
  # codesign: `-dv` reports the authority under test; anything else is a real
  # signing call and just succeeds.
  #
  # THE OUTPUT SHAPE IS LOAD-BEARING, not decoration. The real
  # `codesign -dv --verbose=4` writes 28 unbuffered lines to stderr with the
  # `Authority=` block at line 20 (measured against
  # .build/irrlicht-prod-backup/Irrlicht.app). A one-line stub fits a single
  # write, so `… | grep -q` never SIGPIPEs it and the pipeline returns 0 — which
  # made GATE 3a pass against a script whose Developer-ID check could not
  # succeed at all under `set -o pipefail` (rc 141, 5/5; #1855 review F1/F2).
  # A stub that cannot reproduce the subject's real I/O shape is a stub that
  # certifies the wrong thing.
  cat > "$b/codesign" <<'STUB'
#!/bin/sh
printf 'codesign %s\n' "$*" >>"$STUB_LOG"
case "$1" in
  -dv)
    i=1
    while [ "$i" -le 19 ]; do printf 'Filler-%02d=padding to the real tool line count\n' "$i" >&2; i=$((i + 1)); done
    printf '%s\n' "${STUB_CODESIGN_AUTHORITY:-Authority=Irrlicht Dev}" >&2
    printf 'Authority=Developer ID Certification Authority\n' >&2
    printf 'Authority=Apple Root CA\n' >&2
    i=1
    while [ "$i" -le 6 ]; do printf 'Tail-%02d=lines after the Authority block\n' "$i" >&2; i=$((i + 1)); done
    ;;
esac
exit 0
STUB
  # security: no "Irrlicht Dev" identity, so the ad-hoc signing branch is taken.
  printf '#!/bin/sh\nexit 0\n' > "$b/security"
  # sleep: instant, so the real polling loops run at full fidelity in no time.
  printf '#!/bin/sh\nexit 0\n' > "$b/sleep"
  # PlistBuddy is reached by absolute path, so it gets a seam of its own.
  printf '#!/bin/sh\nprintf "plistbuddy %%s\\n" "$*" >>"$STUB_LOG"\nexit 0\n' > "$root/plistbuddy"
  chmod +x "$b"/* "$root/plistbuddy"

  : > "$root/stub.log"
  echo "$root"
}

# with_backup <root> — the common precondition for every case that is NOT
# about the backup gates: a backup exists, so the no-safety-net refusal does
# not fire first and mask the gate actually under test.
with_backup() { mkdir -p "$1/main/.build/irrlicht-prod-backup/Irrlicht.app"; }

# run_script <root> [VAR=VAL ...] -- [script args...]
# Sets OUT and ST. Never aborts: a non-zero status is the expected outcome of
# most cases here.
run_script() {
  local root="$1"; shift
  local extra=()
  while [[ $# -gt 0 && "$1" != "--" ]]; do extra+=("$1"); shift; done
  [[ "${1:-}" == "--" ]] && shift
  OUT=$(env -i \
    HOME="$root/home" \
    PATH="$root/bin:/usr/bin:/bin:/usr/sbin:/sbin" \
    STUB_LOG="$root/stub.log" \
    IRRLICHT_TESTMAC_REPO_ROOT="$root/repo" \
    IRRLICHT_TESTMAC_MAIN_REPO="$root/main" \
    IRRLICHT_TESTMAC_PROD_APP="$root/Applications/Irrlicht.app" \
    IRRLICHT_TESTMAC_DEV_APP="$root/IrrlichtDev.app" \
    IRRLICHT_TESTMAC_LOG_DIR="$root/logs" \
    IRRLICHT_TESTMAC_PLISTBUDDY="$root/plistbuddy" \
    ${extra[@]+"${extra[@]}"} \
    bash "$REPO_ROOT/$SCRIPT" "$@" 2>&1)
  ST=$?
  return 0
}

flat() { printf '%s' "$1" | tr '\n' ' '; }
sentinel_intact() { [[ "$(cat "$1/Applications/Irrlicht.app/Contents/MacOS/Irrlicht" 2>/dev/null)" == PRODUCTION ]]; }
sparkle_intact()  { [[ "$(cat "$1/Applications/Irrlicht.app/Contents/Frameworks/Sparkle.framework/marker" 2>/dev/null)" == PRODUCTION ]]; }
logged()          { grep -qF -- "$2" "$1/stub.log" 2>/dev/null; }

# ─── happy: the whole run completes — the vacuity guard for everything else ──
# Without this, every abort case could be passing because the fixture is
# broken rather than because a gate fired.
case_happy() {
  local R; R=$(new_env); with_backup "$R"
  run_script "$R" -- replace full
  if [[ $ST -ne 0 ]]; then
    fail "the happy path (replace full) completes — it exited $ST, so every abort case proves nothing: $(flat "$OUT")"
    return
  fi
  pass "the happy path (replace full) completes"
  if logged "$R" 'open '; then pass "...and the app was launched"; else
    fail "the happy path launches the app — no 'open' call was recorded: $(flat "$OUT")"; fi
  if [[ "$(cat "$R/Applications/Irrlicht.app/Contents/MacOS/Irrlicht")" == FRESH-BUILD ]]; then
    pass "...and the dev build really was installed over the bundle"
  else
    fail "the happy path installs the dev build — the bundle executable was not replaced"
  fi
}

# ─── GATE 1: daemon reachability, before the app is launched ────────────────
# If the app starts while no --record daemon answers on 7837, it runs
# `pkill -x irrlichd` and respawns one WITHOUT --record, silently defeating the
# whole run. A gate, not a courtesy sleep.
case_reachability() {
  local R; R=$(new_env); with_backup "$R"
  run_script "$R" STUB_CURL_RC=1 -- replace full
  if [[ $ST -eq 0 ]]; then
    fail "GATE daemon-reachability: an unreachable daemon must abort, but the script exited 0: $(flat "$OUT")"
  elif [[ "$OUT" != *"never became reachable"* ]]; then
    fail "GATE daemon-reachability: aborted (exit $ST) but not with the reachability message: $(flat "$OUT")"
  elif logged "$R" 'open '; then
    fail "GATE daemon-reachability: it aborted, but the app was launched anyway — the app would pkill our daemon and respawn one without --record"
  else
    pass "GATE daemon-reachability: no reachable daemon ⇒ abort, and the app is never launched"
  fi
}

# ─── GATE 2: wait for the app to exit before overwriting its bundle ─────────
case_app_exit() {
  local R; R=$(new_env); with_backup "$R"
  run_script "$R" STUB_PGREP_RC=0 -- replace full
  if [[ $ST -eq 0 ]]; then
    fail "GATE app-exit-wait: a still-running app must abort, but the script exited 0: $(flat "$OUT")"
  elif [[ "$OUT" != *"still running"* ]]; then
    fail "GATE app-exit-wait: aborted (exit $ST) but not with the still-running message: $(flat "$OUT")"
  elif ! sentinel_intact "$R"; then
    fail "GATE app-exit-wait: it aborted, but the bundle executable was overwritten while the process was still alive"
  else
    pass "GATE app-exit-wait: a live app process ⇒ abort, and its bundle is left untouched"
  fi
}

# ─── GATE 3a: backup freshness — refresh a Developer-ID-signed original ─────
# Never trust an existing backup blindly: it can predate a newer production
# release installed since, which would make restore-prod.sh silently reinstall
# a stale build.
case_backup_refresh() {
  local R BACKED; R=$(new_env)
  mkdir -p "$R/main/.build/irrlicht-prod-backup/Irrlicht.app/Contents/MacOS"
  echo 'STALE-BACKUP' > "$R/main/.build/irrlicht-prod-backup/Irrlicht.app/Contents/MacOS/Irrlicht"
  run_script "$R" STUB_CODESIGN_AUTHORITY="Authority=Developer ID Application: Ingo" -- replace full
  BACKED="$(cat "$R/main/.build/irrlicht-prod-backup/Irrlicht.app/Contents/MacOS/Irrlicht" 2>/dev/null)"
  if [[ $ST -ne 0 ]]; then
    fail "GATE backup-freshness: a Developer-ID-signed app must be backed up and installed over, but it exited $ST: $(flat "$OUT")"
  elif [[ "$BACKED" != PRODUCTION ]]; then
    fail "GATE backup-freshness: the stale backup was NOT refreshed from the untouched original (backup holds '$BACKED', wanted PRODUCTION) — restore-prod.sh would reinstall a stale build"
  else
    pass "GATE backup-freshness: a Developer-ID-signed original refreshes the backup before being overwritten"
  fi
}

# ─── GATE 3b: backup freshness — refuse dev-signed with NO backup ───────────
case_backup_refuse() {
  local R; R=$(new_env)   # default authority is "Irrlicht Dev", and no backup dir is created
  run_script "$R" -- replace full
  if [[ $ST -eq 0 ]]; then
    fail "GATE backup-refusal: dev-signed app + no backup must refuse, but the script exited 0: $(flat "$OUT")"
  elif [[ "$OUT" != *"no backup exists"* ]]; then
    fail "GATE backup-refusal: aborted (exit $ST) but not with the no-backup message: $(flat "$OUT")"
  elif ! sentinel_intact "$R"; then
    fail "GATE backup-refusal: it refused, but the only remaining copy of the bundle was overwritten anyway"
  else
    pass "GATE backup-refusal: dev-signed app with no backup ⇒ refuse, with the last copy left intact"
  fi
}

# ─── GATE 4: build-output existence, checked before rm -rf'ing Sparkle ──────
case_build_output() {
  local R; R=$(new_env); with_backup "$R"
  rm -f "$R/repo/platforms/macos/.build/arm64-apple-macosx/debug/Irrlicht"
  run_script "$R" -- replace full
  if [[ $ST -eq 0 ]]; then
    fail "GATE build-output-exists: a missing build product must abort, but the script exited 0: $(flat "$OUT")"
  elif [[ "$OUT" != *"did not produce"* ]]; then
    fail "GATE build-output-exists: aborted (exit $ST) but not with the missing-build-output message: $(flat "$OUT")"
  elif ! sparkle_intact "$R"; then
    fail "GATE build-output-exists: it aborted, but Sparkle.framework was already deleted with nothing to replace it"
  else
    pass "GATE build-output-exists: a missing build product ⇒ abort, with Sparkle.framework left in place"
  fi
}

# ─── GATE 5: MODE/TARGET validated against the literal enum, no default ─────
case_enum() {
  local R; R=$(new_env)
  # A backup exists, so a typo that slipped through would run to completion
  # rather than being stopped by a later gate — which is what makes "it exited
  # 0" below the honest description of the hazard.
  with_backup "$R"
  run_script "$R" -- seperate
  if [[ $ST -eq 0 ]]; then
    fail "GATE enum-validation: the typo 'seperate' must be refused, but the script exited 0 and would have silently run in replace mode: $(flat "$OUT")"
  elif [[ "$OUT" != *"unrecognised argument"* ]]; then
    fail "GATE enum-validation: refused (exit $ST) but not with the unrecognised-argument message: $(flat "$OUT")"
  else
    pass "GATE enum-validation: an unrecognised axis value is refused instead of silently no-opping every step"
  fi
  # Vacuity guard: a validator that rejected EVERYTHING would satisfy the case
  # above while making the script unusable.
  R=$(new_env)
  run_script "$R" -- separate daemon
  if [[ "$OUT" == *"unrecognised argument"* ]]; then
    fail "GATE enum-validation: the valid pair 'separate daemon' was rejected — the validator rejects everything: $(flat "$OUT")"
  elif [[ "$OUT" != *"MODE=separate TARGET=daemon"* ]]; then
    fail "GATE enum-validation: 'separate daemon' did not resolve to MODE=separate TARGET=daemon: $(flat "$OUT")"
  else
    pass "GATE enum-validation: ...and the valid spellings are still accepted, in either order"
  fi
}

# ─── GATE 6: the app is killed BEFORE the daemon ────────────────────────────
# So a live app never observes a momentary daemon-less gap and reacts by
# spawning its own replacement, which could win the port race against the
# daemon the script starts next.
case_kill_order() {
  local R APP_LINE DMN_LINE; R=$(new_env); with_backup "$R"
  run_script "$R" -- replace full
  APP_LINE=$(grep -nF -- 'pkill -f Irrlicht\.app/Contents/MacOS/Irrlicht' "$R/stub.log" 2>/dev/null | head -1 | cut -d: -f1)
  DMN_LINE=$(grep -nF -- 'pkill -x irrlichd' "$R/stub.log" 2>/dev/null | head -1 | cut -d: -f1)
  if [[ -z "$APP_LINE" || -z "$DMN_LINE" ]]; then
    fail "GATE kill-order: COULD NOT LOOK — the app pkill (line '${APP_LINE:-none}') and/or the daemon pkill (line '${DMN_LINE:-none}') never happened, so their order proves nothing. Log: $(flat "$(cat "$R/stub.log")")"
  elif [[ "$APP_LINE" -ge "$DMN_LINE" ]]; then
    fail "GATE kill-order: the daemon was killed at line $DMN_LINE, at or before the app at line $APP_LINE — a live app can observe the daemon-less gap and spawn its own replacement"
  else
    pass "GATE kill-order: the app is killed (line $APP_LINE) before the daemon (line $DMN_LINE)"
  fi
}

# ─── GATE 7 (ADDED by #1855): build freshness ───────────────────────────────
# Existence is not freshness. A product left over from an earlier compile —
# including one compiled while a tools/mutate.sh mutation was applied — is a
# perfectly valid binary and passes GATE 4, so installing it means debugging a
# defect the source does not have. That happened on this machine.
case_freshness() {
  local R; R=$(new_env); with_backup "$R"
  touch -t 202601030101 "$R/repo/platforms/macos/Irrlicht/App.swift"   # source now NEWER than the product
  run_script "$R" -- replace full
  if [[ $ST -eq 0 ]]; then
    fail "GATE build-freshness: a source newer than the build product must abort, but the script exited 0 and installed a stale binary: $(flat "$OUT")"
  elif [[ "$OUT" != *"NEWER than the built binary"* ]]; then
    fail "GATE build-freshness: aborted (exit $ST) but not with the staleness message: $(flat "$OUT")"
  elif ! sentinel_intact "$R"; then
    fail "GATE build-freshness: it aborted, but the stale binary was installed over the bundle anyway"
  else
    pass "GATE build-freshness: a source newer than the build product ⇒ abort, with the bundle left untouched"
  fi
}

# ...and the freshness check must REFUSE when it cannot run, rather than pass
# vacuously against an empty source set (AGENTS.md: absence of a finding and
# inability to look must never produce the same output).
case_freshness_vacuous() {
  local R; R=$(new_env); with_backup "$R"
  find "$R/repo/platforms/macos/Irrlicht" -name '*.swift' -delete
  run_script "$R" -- replace full
  if [[ $ST -eq 0 ]]; then
    fail "GATE build-freshness: with no .swift sources the check cannot run, but the script exited 0 — a vacuous pass: $(flat "$OUT")"
  elif [[ "$OUT" != *"no .swift sources found"* ]]; then
    fail "GATE build-freshness: refused (exit $ST) but not with the cannot-run message: $(flat "$OUT")"
  else
    pass "GATE build-freshness: ...and it REFUSES when it has nothing to compare against"
  fi
}

# ─── GATE 8 (ADDED by #1855): a failed swift build never reaches the bundle ─
# The pre-script procedure ran `swift build 2>&1 | tail -5`, whose exit status
# is tail's. A failed build reported success and the run continued into the
# install with whatever binary happened to be lying around.
case_swift_status() {
  local R; R=$(new_env); with_backup "$R"
  run_script "$R" STUB_SWIFT_RC=1 -- replace full
  if [[ $ST -eq 0 ]]; then
    fail "GATE swift-build-status: a failed swift build must abort, but the script exited 0 (its status came from the pipe, not from swift): $(flat "$OUT")"
  elif [[ "$OUT" != *"swift build failed"* ]]; then
    fail "GATE swift-build-status: aborted (exit $ST) but not with the build-failed message: $(flat "$OUT")"
  elif ! sentinel_intact "$R"; then
    fail "GATE swift-build-status: it aborted, but the bundle was overwritten anyway"
  else
    pass "GATE swift-build-status: a failed swift build ⇒ abort, with the bundle left untouched"
  fi
}

# ─── The TARGET axis actually skips the component it names ──────────────────
case_target_daemon() {
  local R; R=$(new_env); with_backup "$R"
  run_script "$R" -- replace daemon
  if [[ $ST -ne 0 ]]; then
    fail "TARGET=daemon completes — it exited $ST: $(flat "$OUT")"
  elif logged "$R" 'open '; then
    fail "TARGET=daemon must not touch the app, but it launched one: $(flat "$(cat "$R/stub.log")")"
  elif ! sentinel_intact "$R"; then
    fail "TARGET=daemon must not touch the app bundle, but the executable was overwritten"
  else
    pass "TARGET=daemon rebuilds the daemon and leaves the app alone"
  fi
}

case_target_macos() {
  local R; R=$(new_env); with_backup "$R"
  run_script "$R" -- replace macos   # no daemon started; one must already be reachable
  if [[ $ST -ne 0 ]]; then
    fail "TARGET=macos completes against a reachable daemon — it exited $ST: $(flat "$OUT")"
  elif logged "$R" 'nohup '; then
    fail "TARGET=macos must not start a daemon, but it did: $(flat "$(cat "$R/stub.log")")"
  else
    pass "TARGET=macos relaunches the app without starting a daemon"
  fi
}

case_target_macos_nodaemon() {
  local R; R=$(new_env); with_backup "$R"
  run_script "$R" STUB_CURL_RC=1 -- replace macos
  if [[ $ST -eq 0 ]]; then
    fail "TARGET=macos with no reachable daemon must abort, but it exited 0: $(flat "$OUT")"
  elif [[ "$OUT" != *"doesn't start one"* ]]; then
    fail "TARGET=macos aborted (exit $ST) but not with the no-daemon message: $(flat "$OUT")"
  elif logged "$R" 'open '; then
    fail "TARGET=macos aborted, but launched the app anyway"
  else
    pass "TARGET=macos with no reachable daemon ⇒ abort, and the app is never launched"
  fi
}

# ─── CALL SITES, not just gate definitions ─────────────────────────────────
# This change is an EXTRACTION, so the risk moved to the call sites: the values
# each step passes. A battery of fourteen mutations found eleven survivors and
# every one was a call site — `--record` dropped from the daemon launch, the
# replace-mode port changed to 7838, IRRLICHT_BIND_ADDR removed, the whole
# `separate` bundle assembly never executed, install_name_tool deleted, the
# socket cleanup deleted (#1855 review F3). Gates say "it refuses correctly";
# these say "it does the right thing when it proceeds".

# The daemon is the whole point of the run: a daemon without --record, or on
# the wrong port, silently produces nothing while every gate above stays green.
case_daemon_launch() {
  local R L; R=$(new_env); with_backup "$R"
  run_script "$R" -- replace full
  L="$(grep '^nohup ' "$R/stub.log" 2>/dev/null | head -1)"
  if [[ -z "$L" ]]; then
    fail "CALL-SITE daemon-launch: COULD NOT LOOK — the daemon was never launched, so its arguments prove nothing. Log: $(flat "$(cat "$R/stub.log")")"
    return
  fi
  [[ "$L" == *"--record"* ]] \
    && pass "CALL-SITE daemon-launch: replace mode passes --record" \
    || fail "CALL-SITE daemon-launch: the daemon was started WITHOUT --record — the run records nothing, and every reachability gate still passes: $L"
  [[ "$L" == *"BIND=127.0.0.1:7837"* ]] \
    && pass "CALL-SITE daemon-launch: ...on the production port, via IRRLICHT_BIND_ADDR" \
    || fail "CALL-SITE daemon-launch: replace mode did not bind 127.0.0.1:7837 — a missing PORT makes the daemon bind '127.0.0.1:' (invalid), the exact silent failure this script exists to remove: $L"
  [[ "$L" == *"IRRHOME=<unset>"* ]] \
    && pass "CALL-SITE daemon-launch: ...with no IRRLICHT_HOME override, so it reads production state" \
    || fail "CALL-SITE daemon-launch: replace mode passed an IRRLICHT_HOME override, so it would not see production's sessions/cost data: $L"
}

# separate mode's ENTIRE bundle assembly and its open --env launch had no case
# at all: the only `separate` run was `separate daemon`, which skips every app
# step. Half the MODE axis was unexercised.
case_separate_full() {
  local R L APP; R=$(new_env)
  run_script "$R" -- separate full
  APP="$R/IrrlichtDev.app"
  if [[ $ST -ne 0 ]]; then
    fail "CALL-SITE separate-full: the run must complete, but it exited $ST: $(flat "$OUT")"
    return
  fi
  [[ "$(cat "$APP/Contents/MacOS/Irrlicht" 2>/dev/null)" == FRESH-BUILD ]] \
    && pass "CALL-SITE separate-full: the freshly built binary is installed into the dev bundle" \
    || fail "CALL-SITE separate-full: $APP/Contents/MacOS/Irrlicht is not the built binary — the dev app would run whatever was there before, or not launch at all"
  [[ -d "$APP/Contents/Frameworks/Sparkle.framework" ]] \
    && pass "CALL-SITE separate-full: ...with Sparkle.framework embedded" \
    || fail "CALL-SITE separate-full: Sparkle.framework is missing from the assembled bundle — dyld cannot resolve it and the app crashes at launch"
  grep -q 'io.irrlicht.app' "$APP/Contents/Info.plist" 2>/dev/null \
    && pass "CALL-SITE separate-full: ...and an Info.plist carrying the bundle identifier" \
    || fail "CALL-SITE separate-full: the assembled Info.plist has no io.irrlicht.app identifier — UNUserNotificationCenter (the reason the bundle is assembled at all) will not work"
  L="$(grep '^open ' "$R/stub.log" 2>/dev/null | head -1)"
  if [[ "$L" != *"IRRLICHT_DAEMON_PORT=7838"* ]]; then
    fail "CALL-SITE separate-full: the dev app was launched without --env IRRLICHT_DAEMON_PORT=7838, so it would talk to PRODUCTION on 7837: ${L:-<no open call>}"
  else
    pass "CALL-SITE separate-full: the dev app is pointed at the isolated daemon on 7838"
  fi
  L="$(grep '^nohup ' "$R/stub.log" 2>/dev/null | head -1)"
  if [[ "$L" != *"BIND=127.0.0.1:7838"* || "$L" != *"PERM=grant-all"* || "$L" != *"IRRHOME=$R/repo/.build/irrlicht-home"* ]]; then
    fail "CALL-SITE separate-full: the isolated daemon was not started on 7838 with an isolated IRRLICHT_HOME and grant-all (#570: without grant-all a fresh state dir monitors nothing): ${L:-<no nohup call>}"
  else
    pass "CALL-SITE separate-full: the isolated daemon gets 7838 + its own IRRLICHT_HOME + grant-all"
  fi
}

# install_name_tool MUST run before codesign — it mutates the binary and so
# invalidates any signature applied first. The script says so; nothing checked.
case_sign_order() {
  local R IN CS; R=$(new_env); with_backup "$R"
  run_script "$R" -- replace full
  IN=$(grep -n '^install_name_tool ' "$R/stub.log" 2>/dev/null | head -1 | cut -d: -f1)
  CS=$(grep -n '^codesign --force' "$R/stub.log" 2>/dev/null | head -1 | cut -d: -f1)
  if [[ -z "$IN" || -z "$CS" ]]; then
    fail "CALL-SITE sign-order: COULD NOT LOOK — install_name_tool (line '${IN:-none}') and/or the signing codesign (line '${CS:-none}') never ran, so their order proves nothing. Log: $(flat "$(cat "$R/stub.log")")"
  elif [[ "$IN" -ge "$CS" ]]; then
    fail "CALL-SITE sign-order: codesign ran at line $CS, at or before install_name_tool at line $IN — the rpath edit invalidates the signature just applied"
  else
    pass "CALL-SITE sign-order: install_name_tool (line $IN) runs before codesign (line $CS)"
  fi
  if grep -q '^codesign --force.*--entitlements' "$R/stub.log" 2>/dev/null; then
    pass "CALL-SITE sign-order: ...and the signature carries the dev entitlements file"
  else
    fail "CALL-SITE sign-order: codesign was called without --entitlements — the dev build loses the entitlements the app needs: $(flat "$(grep '^codesign --force' "$R/stub.log")")"
  fi
}

# The two remaining install-time writes, and the socket cleanup — all three
# were surviving mutations.
case_install_details() {
  local R; R=$(new_env); with_backup "$R"
  run_script "$R" -- replace full
  [[ "$(cat "$R/Applications/Irrlicht.app/Contents/Resources/AppIcon.icns" 2>/dev/null)" == icns ]] \
    && pass "CALL-SITE install-details: the icon is refreshed from the worktree's Resources/" \
    || fail "CALL-SITE install-details: AppIcon.icns still holds the production copy — a developer iterating on Resources/ sees a stale icon in replace mode but not in separate mode"
  [[ ! -e "$R/home/.local/share/irrlicht/irrlichd.sock" ]] \
    && pass "CALL-SITE install-details: the stale socket is removed before the daemon restarts" \
    || fail "CALL-SITE install-details: the stale socket survived — this is the \$SOCK the old fenced blocks dropped, turning cleanup into a silent no-op"
  grep -q '^plistbuddy .*CFBundleShortVersionString dev' "$R/stub.log" 2>/dev/null \
    && pass "CALL-SITE install-details: the version string is stamped to 'dev'" \
    || fail "CALL-SITE install-details: CFBundleShortVersionString was not stamped — Settings/About shows whatever release version was last installed on a freshly compiled dev build"
}

# ─── F5: an abort AFTER the bundle is overwritten must say how to recover ───
case_abort_hint() {
  local R; R=$(new_env); with_backup "$R"
  run_script "$R" STUB_OPEN_RC=1 -- replace full
  if [[ $ST -eq 0 ]]; then
    fail "GATE abort-hint: a failing launch after the install must not exit 0 — the bundle now holds a dev build: $(flat "$OUT")"
  elif [[ "$OUT" != *"now holds a DEV build"* || "$OUT" != *"restore-prod.sh"* ]]; then
    fail "GATE abort-hint: it failed (exit $ST) after overwriting the production bundle but never said so, nor pointed at restore-prod.sh — the user is left with a silently dev-ified production bundle: $(flat "$OUT")"
  else
    pass "GATE abort-hint: a failure after the install names the dev-ified bundle and points at restore-prod.sh"
  fi
  # ...and it must NOT cry wolf on a run that never touched the bundle.
  R=$(new_env); with_backup "$R"
  run_script "$R" STUB_CURL_RC=1 -- replace daemon
  if [[ "$OUT" == *"now holds a DEV build"* ]]; then
    fail "GATE abort-hint: a TARGET=daemon run never touches the bundle, but it still told the user to restore production: $(flat "$OUT")"
  else
    pass "GATE abort-hint: ...and stays quiet when the bundle was never written"
  fi
}

# ─── The seams are honoured BEHAVIOURALLY, not just present as text ────────
# The text guard at the top would pass on a seam that survives only in a
# comment, at which point every case silently starts driving the real
# /Applications/Irrlicht.app. This case makes the script name the redirected
# path in its own error: a message naming the fixture root can only come from
# the override actually being read.
case_seams() {
  local R; R=$(new_env)
  rm -rf "$R/Applications/Irrlicht.app"
  run_script "$R" -- replace full
  if [[ $ST -eq 0 ]]; then
    fail "SEAMS: a missing production bundle must abort, but the script exited 0: $(flat "$OUT")"
  elif [[ "$OUT" != *"$R/Applications/Irrlicht.app is not installed"* ]]; then
    fail "SEAMS: the script did not name the FIXTURE bundle ($R/Applications/Irrlicht.app) — it is not reading \$IRRLICHT_TESTMAC_PROD_APP, so every case in this file is pointed at the real machine: $(flat "$OUT")"
  else
    pass "SEAMS: test-mac.sh resolves \$IRRLICHT_TESTMAC_PROD_APP to the fixture, not /Applications"
  fi
  R=$(new_env)
  rm -rf "$R/Applications/Irrlicht.app"
  run_restore "$R"
  if [[ "$OUT" != *"$R/Applications/Irrlicht.app"* ]]; then
    fail "SEAMS: restore-prod.sh did not name the FIXTURE bundle — it is not reading \$IRRLICHT_TESTMAC_PROD_APP, so the restore cases operate on the real /Applications/Irrlicht.app: $(flat "$OUT")"
  else
    pass "SEAMS: restore-prod.sh does too"
  fi
}

# ─── restore-prod.sh — the teardown half, carrying the same F1 hazard ───────
# It had no test at all. These two cases exist because the identical
# `codesign … | grep -q` pipeline was in it, under its own `set -o pipefail`.
run_restore() {
  local root="$1"; shift
  OUT=$(env -i \
    HOME="$root/home" \
    PATH="$root/bin:/usr/bin:/bin:/usr/sbin:/sbin" \
    STUB_LOG="$root/stub.log" \
    IRRLICHT_TESTMAC_MAIN_REPO="$root/main" \
    IRRLICHT_TESTMAC_PROD_APP="$root/Applications/Irrlicht.app" \
    IRRLICHT_TESTMAC_PORT=7837 \
    ${1+"$@"} \
    bash "$REPO_ROOT/.claude/skills/ir:test-mac/restore-prod.sh" 2>&1)
  ST=$?
  return 0
}

case_restore_signed() {
  local R; R=$(new_env)   # no backup dir; the app is genuinely Developer-ID-signed
  run_restore "$R" STUB_CODESIGN_AUTHORITY="Authority=Developer ID Application: Ingo"
  if [[ $ST -ne 0 ]]; then
    fail "restore-prod: a genuine Developer-ID app with no backup means there is nothing to restore, but it exited $ST — it kills the app and daemon and THEN tells the user to reinstall a correctly installed app: $(flat "$OUT")"
  elif [[ "$OUT" != *"nothing to restore"* ]]; then
    fail "restore-prod: exited 0 but did not report 'nothing to restore': $(flat "$OUT")"
  elif ! logged "$R" 'open '; then
    fail "restore-prod: it reported success without ever launching production: $(flat "$(cat "$R/stub.log")")"
  else
    pass "restore-prod: a genuine production app with no backup ⇒ nothing to restore, and production is relaunched"
  fi
}

case_restore_refuse() {
  local R; R=$(new_env)   # default authority is "Irrlicht Dev", and no backup
  run_restore "$R"
  if [[ $ST -eq 0 ]]; then
    fail "restore-prod: a dev-signed app with no backup cannot be restored and must refuse, but it exited 0: $(flat "$OUT")"
  elif [[ "$OUT" != *"Cannot confirm production is intact"* ]]; then
    fail "restore-prod: refused (exit $ST) but not with the cannot-confirm message: $(flat "$OUT")"
  else
    pass "restore-prod: a dev-signed app with no backup ⇒ refuse, naming the reinstall path"
  fi
}

# ─── The gate that runs this file must fire on this file's SUBJECT ──────────
# tools/preflight.sh --changed scopes the `tools` gate by a trigger regex. The
# script under test lives outside tools/, so without an explicit alternative a
# push that changes ONLY that script skips this whole file — and a gate that
# never ran is indistinguishable from one that found nothing.
case_preflight_trigger() {
  local PF=tools/preflight.sh tools_re probe probe_fails=0
  tools_re=$(grep -a "run_gate_scoped '\^tools/lib/" "$PF" \
             | sed -E "s/^[[:space:]]*run_gate_scoped '//; s/'[[:space:]]*\\\\?[[:space:]]*$//")
  if [[ -z "$tools_re" ]]; then
    fail "preflight-trigger: COULD NOT LOOK — no run_gate_scoped line starting ^tools/lib/ in $PF; the scan has gone blind, not the trigger wrong"
    return
  fi
  for probe in "$SCRIPT" ".claude/skills/ir:test-mac/restore-prod.sh" "tools/lib/$NAME.sh"; do
    if ! printf '%s\n' "$probe" | grep -qE "$tools_re"; then
      fail "preflight-trigger: the tools gate does NOT fire on a diff touching $probe — this file would be skipped on the very push that changes its subject. Regex: $tools_re"
      probe_fails=1
    fi
  done
  # Vacuity guard: a trigger matching everything would satisfy every probe
  # above while scoping nothing.
  if printf '%s\n' core/domain/session.go | grep -qE "$tools_re"; then
    fail "preflight-trigger: the tools-gate regex matches core/domain/session.go too — it has stopped scoping: $tools_re"
  elif [[ $probe_fails -eq 0 ]]; then
    pass "preflight-trigger: the tools gate fires on the script, its teardown half and this test, and still scopes"
  fi
}

# ─── dispatch ───────────────────────────────────────────────────────────────
CASES=(happy reachability app-exit backup-refresh backup-refuse build-output
       enum kill-order freshness freshness-vacuous swift-status
       target-daemon target-macos target-macos-nodaemon
       daemon-launch separate-full sign-order install-details abort-hint
       seams restore-signed restore-refuse preflight-trigger)

echo "== $NAME: $SCRIPT${CASE_FILTER:+ (case: $CASE_FILTER)} =="

ran=0
for c in "${CASES[@]}"; do
  [[ -n "$CASE_FILTER" && "$CASE_FILTER" != "$c" ]] && continue
  "case_${c//-/_}"
  ran=$((ran + 1))
done

# A filter that matches nothing must NEVER read as a clean run: a typo in a
# mutation fixture's case name would otherwise produce an "ALL PASS" over zero
# assertions, which is the exact shape AGENTS.md forbids.
if [[ $ran -eq 0 ]]; then
  echo "FAIL: $NAME — case filter '$CASE_FILTER' matched none of: ${CASES[*]}. Nothing was checked." >&2
  exit 2
fi

echo
if [[ $fails -eq 0 ]]; then
  echo "$NAME: ALL PASS ($ran case(s))"
  exit 0
fi
echo "$NAME: $fails FAILED ($ran case(s))" >&2
exit 1
