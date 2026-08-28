#!/usr/bin/env bash
# test-mac-script_test.sh — the LOCK TEST for the gates in
# .claude/skills/ir:test-mac/test-mac.sh (#1855).
#
# WHAT THIS IS. #1855 turned nine fenced bash blocks in that skill's SKILL.md
# into one script. Six gates were MOVED across in that extraction and one was
# added; each has an incident behind it and several fail SILENTLY when broken,
# which is exactly why an extraction is where they get lost. This file drives
# the real script end to end and asserts each gate still holds.
# tools/lib/test-mac-script-mutations_test.sh then breaks each gate in turn
# and requires THIS file to go red — a green that was never red is a claim,
# not evidence (AGENTS.md, Testing).
#
# SAFETY. The script under test kills processes and overwrites
# /Applications/Irrlicht.app. Four things keep it off this machine, and every
# one of them is load-bearing:
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
# Convention follows tools/lib/install-uninstall_test.sh and
# tools/lib/agents-md-lint_test.sh: plain bash, `set -uo pipefail` (never -e —
# a non-zero status from the subject is DATA here, half the cases expect one),
# a `fails` counter, and "ALL PASS" / "N FAILED" at the end. FAIL lines start
# at column 0 because tools/lib/mutation-assert.sh greps `^FAIL:` to decide
# whether a mutation went red.
set -uo pipefail

NAME=test-mac-script_test
REPO_ROOT=$(git rev-parse --show-toplevel) || { echo "FAIL: $NAME — not inside a git repo" >&2; exit 2; }
cd "$REPO_ROOT" || { echo "FAIL: $NAME — cannot cd to $REPO_ROOT" >&2; exit 2; }

SCRIPT=".claude/skills/ir:test-mac/test-mac.sh"

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
# the script stops honouring them, this file would silently start driving the
# real /Applications/Irrlicht.app — so refuse to run rather than find out.
for seam in IRRLICHT_TESTMAC_REPO_ROOT IRRLICHT_TESTMAC_MAIN_REPO IRRLICHT_TESTMAC_PROD_APP \
            IRRLICHT_TESTMAC_DEV_APP IRRLICHT_TESTMAC_LOG_DIR IRRLICHT_TESTMAC_PLISTBUDDY; do
  if ! grep -q "$seam" "$REPO_ROOT/$SCRIPT"; then
    echo "FAIL: $NAME — REFUSING TO RUN: $SCRIPT no longer honours \$$seam." >&2
    echo "      Without it this test drives the real production app and daemon." >&2
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
  root="$(mktemp -d "$WORK/env.XXXXXX")" || return 1
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
  for s in pkill open nohup install_name_tool; do
    printf '#!/bin/sh\nprintf "%%s %%s\\n" "%s" "$*" >>"$STUB_LOG"\nexit 0\n' "$s" > "$b/$s"
  done
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
  cat > "$b/codesign" <<'STUB'
#!/bin/sh
printf 'codesign %s\n' "$*" >>"$STUB_LOG"
case "$1" in
  -dv) printf '%s\n' "${STUB_CODESIGN_AUTHORITY:-Authority=Irrlicht Dev}" >&2 ;;
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

# run_script <root> [VAR=VAL ...] -- [script args...]
# Sets OUT and ST. Never aborts: a non-zero status is the expected outcome of
# half the cases here.
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

echo "== $NAME: $SCRIPT =="

# ─── 0. The happy path runs clean — the vacuity guard for everything below ──
# Without this, every abort case could be passing because the fixture is
# broken rather than because a gate fired.
R=$(new_env)
mkdir -p "$R/main/.build/irrlicht-prod-backup/Irrlicht.app"   # dev-signed + a backup ⇒ the refusal below does not fire
run_script "$R" -- replace full
if [[ $ST -ne 0 ]]; then
  fail "the happy path (replace full) completes — it exited $ST, so every abort case below proves nothing: $(flat "$OUT")"
else
  pass "the happy path (replace full) completes"
  if logged "$R" 'open '; then pass "...and the app was launched"; else
    fail "the happy path launches the app — no 'open' call was recorded: $(flat "$OUT")"; fi
  if [[ "$(cat "$R/Applications/Irrlicht.app/Contents/MacOS/Irrlicht")" == FRESH-BUILD ]]; then
    pass "...and the dev build really was installed over the bundle"
  else
    fail "the happy path installs the dev build — the bundle executable was not replaced"
  fi
fi

# ─── GATE 1: daemon reachability, before the app is launched ────────────────
# If the app starts while no --record daemon answers on 7837, it runs
# `pkill -x irrlichd` and respawns one WITHOUT --record, silently defeating the
# whole run. A gate, not a courtesy sleep.
R=$(new_env)
mkdir -p "$R/main/.build/irrlicht-prod-backup/Irrlicht.app"
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

# ─── GATE 2: wait for the app to exit before overwriting its bundle ─────────
R=$(new_env)
mkdir -p "$R/main/.build/irrlicht-prod-backup/Irrlicht.app"
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

# ─── GATE 3a: backup freshness — refresh a Developer-ID-signed original ─────
# Never trust an existing backup blindly: it can predate a newer production
# release installed since, which would make restore-prod.sh silently reinstall
# a stale build.
R=$(new_env)
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

# ─── GATE 3b: backup freshness — refuse dev-signed with NO backup ───────────
R=$(new_env)   # default authority is "Irrlicht Dev", and no backup dir is created
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

# ─── GATE 4: build-output existence, checked before rm -rf'ing Sparkle ──────
R=$(new_env)
mkdir -p "$R/main/.build/irrlicht-prod-backup/Irrlicht.app"
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

# ─── GATE 5: MODE/TARGET validated against the literal enum, no default ─────
R=$(new_env)
# A backup exists, so a typo that slipped through would run to completion
# rather than being stopped by a later gate — which is what makes "it exited 0"
# below the honest description of the hazard.
mkdir -p "$R/main/.build/irrlicht-prod-backup/Irrlicht.app"
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

# ─── GATE 6: the app is killed BEFORE the daemon ────────────────────────────
# So a live app never observes a momentary daemon-less gap and reacts by
# spawning its own replacement, which could win the port race against the
# daemon the script starts next.
R=$(new_env)
mkdir -p "$R/main/.build/irrlicht-prod-backup/Irrlicht.app"
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

# ─── GATE 7 (ADDED by #1855): build freshness ───────────────────────────────
# Existence is not freshness. A product left over from an earlier compile —
# including one compiled while a tools/mutate.sh mutation was applied — is a
# perfectly valid binary and passes GATE 4, so installing it means debugging a
# defect the source does not have. That happened on this machine.
R=$(new_env)
mkdir -p "$R/main/.build/irrlicht-prod-backup/Irrlicht.app"
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
# ...and the freshness check must REFUSE when it cannot run, rather than pass
# vacuously against an empty source set (AGENTS.md: absence of a finding and
# inability to look must never produce the same output).
R=$(new_env)
mkdir -p "$R/main/.build/irrlicht-prod-backup/Irrlicht.app"
find "$R/repo/platforms/macos/Irrlicht" -name '*.swift' -delete
run_script "$R" -- replace full
if [[ $ST -eq 0 ]]; then
  fail "GATE build-freshness: with no .swift sources the check cannot run, but the script exited 0 — a vacuous pass: $(flat "$OUT")"
elif [[ "$OUT" != *"no .swift sources found"* ]]; then
  fail "GATE build-freshness: refused (exit $ST) but not with the cannot-run message: $(flat "$OUT")"
else
  pass "GATE build-freshness: ...and it REFUSES when it has nothing to compare against"
fi

# ─── GATE 8 (ADDED by #1855): a failed swift build never reaches the bundle ─
# The pre-script procedure ran `swift build 2>&1 | tail -5`, whose exit status
# is tail's. A failed build reported success and the run continued into the
# install with whatever binary happened to be lying around.
R=$(new_env)
mkdir -p "$R/main/.build/irrlicht-prod-backup/Irrlicht.app"
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

# ─── TARGET axis: daemon-only and macos-only skip the other component ───────
R=$(new_env)
mkdir -p "$R/main/.build/irrlicht-prod-backup/Irrlicht.app"
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

R=$(new_env)
mkdir -p "$R/main/.build/irrlicht-prod-backup/Irrlicht.app"
run_script "$R" -- replace macos   # no daemon started; one must already be reachable
if [[ $ST -ne 0 ]]; then
  fail "TARGET=macos completes against a reachable daemon — it exited $ST: $(flat "$OUT")"
elif logged "$R" 'nohup '; then
  fail "TARGET=macos must not start a daemon, but it did: $(flat "$(cat "$R/stub.log")")"
else
  pass "TARGET=macos relaunches the app without starting a daemon"
fi

R=$(new_env)
mkdir -p "$R/main/.build/irrlicht-prod-backup/Irrlicht.app"
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

# ─── The gate that runs this file must actually fire on this file's SUBJECT ──
# tools/preflight.sh --changed scopes the `tools` gate by a trigger regex. The
# script under test lives outside tools/, so without an explicit alternative a
# push that changes ONLY that script skips this whole file — and a gate that
# never ran is indistinguishable from one that found nothing.
PF=tools/preflight.sh
tools_re=$(grep -a "run_gate_scoped '\^tools/lib/" "$PF" \
           | sed -E "s/^[[:space:]]*run_gate_scoped '//; s/'[[:space:]]*\\\\?[[:space:]]*$//")
if [[ -z "$tools_re" ]]; then
  fail "preflight-trigger: COULD NOT LOOK — no run_gate_scoped line starting ^tools/lib/ in $PF; the scan has gone blind, not the trigger wrong"
else
  probe_fails=0
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
fi

echo
if [[ $fails -eq 0 ]]; then
  echo "$NAME: ALL PASS"
  exit 0
fi
echo "$NAME: $fails FAILED" >&2
exit 1
