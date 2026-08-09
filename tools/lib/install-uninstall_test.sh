#!/usr/bin/env bash
# Tests for site/install.sh's uninstall path — specifically that it asks
# irrlichd to remove the hook entries it wrote into the user's agent configs
# before it deletes the binary that can do the removing (#1416).
#
# Before #1416 the uninstall removed the LaunchAgent, the systemd unit, the app
# bundle and ~/.local/bin/irrlichd, and never touched ~/.claude/settings.json or
# ~/.codex/hooks.json — so an uninstalled irrlicht stayed in the user's agent
# configuration, pointing at a binary that no longer existed.
#
# SAFETY. Every case runs the real site/install.sh as a subprocess, and that
# script kills processes and deletes install locations. Three things keep it off
# this machine's real install, and all three are load-bearing:
#   1. HOME is a mktemp -d, so ~/.claude, ~/.codex, ~/.local/bin/irrlichd,
#      ~/Library/LaunchAgents/... and ~/.config/systemd/user/... are all fakes.
#   2. IRRLICHT_APP_PATH redirects the one install location that is NOT under
#      $HOME. Without it the script would `rm -rf /Applications/Irrlicht.app`
#      for real — and on CI, where that path does not exist, the test would pass
#      while doing it.
#   3. PATH is fronted by stubs for pgrep/pkill/lsof/launchctl/systemctl, so the
#      process-killing block cannot reach a developer's running daemon or app.
# A case that skips any of the three is a case that can destroy the machine it
# runs on. Build every environment through new_env().
#
# Convention follows tools/lib/changed-files_test.sh: plain bash, hand-rolled
# asserts, a `fails` counter, "ALL PASS" / "N FAILED" at the end.

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
INSTALL_SH="$REPO_ROOT/site/install.sh"
NAME="install-uninstall_test"

fails=0

pass() { printf 'PASS: %s\n' "$1"; }
fail() {
    printf 'FAIL: %s\n' "$1"
    [ $# -gt 1 ] && printf '      %s\n' "$2"
    fails=$((fails + 1))
    return 0
}
assert_contains() {
    local haystack="$1" needle="$2" what="$3"
    case "$haystack" in
        *"$needle"*) pass "$what" ;;
        *) fail "$what" "expected to find [$needle] in: $(printf '%s' "$haystack" | head -c 400)" ;;
    esac
}
assert_not_contains() {
    local haystack="$1" needle="$2" what="$3"
    case "$haystack" in
        *"$needle"*) fail "$what" "did NOT expect [$needle] in: $(printf '%s' "$haystack" | head -c 400)" ;;
        *) pass "$what" ;;
    esac
}
assert_eq() {
    if [[ "$1" == "$2" ]]; then pass "$3"; else fail "$3" "expected [$2] got [$1]"; fi
}
assert_file_absent() {
    if [[ -e "$1" ]]; then fail "$2" "expected [$1] to be gone"; else pass "$2"; fi
}
assert_file_present() {
    if [[ -e "$1" ]]; then pass "$2"; else fail "$2" "expected [$1] to exist"; fi
}

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# ---------------------------------------------------------------------------
# new_env <name> — build an isolated fake machine and echo its root.
#
# Layout:
#   <root>/home                 → HOME
#   <root>/home/.claude/settings.json   dirty: carries an irrlicht hook entry
#   <root>/home/.codex/hooks.json       dirty: carries an irrlicht hook entry
#   <root>/app/Irrlicht.app     → IRRLICHT_APP_PATH (created by the caller if wanted)
#   <root>/bin                  → PATH stubs
#   <root>/marker               → written by the irrlichd stub when it runs
# ---------------------------------------------------------------------------
new_env() {
    local root="$WORK/$1"
    mkdir -p "$root/home/.claude" "$root/home/.codex" "$root/bin" "$root/app"

    # Config files shaped like the real thing: a hook entry naming the binary.
    cat >"$root/home/.claude/settings.json" <<'JSON'
{
  "hooks": {
    "SessionStart": [
      { "hooks": [ { "type": "http", "url": "http://127.0.0.1:7837/hooks/claudecode" } ] }
    ]
  },
  "statusLine": { "type": "command", "command": "irrlichd --statusline" }
}
JSON
    cat >"$root/home/.codex/hooks.json" <<'JSON'
{
  "hooks": {
    "PreToolUse": [ { "command": "irrlichd --hook codex || true" } ]
  }
}
JSON

    # --- PATH stubs. Each one neutralizes a real, destructive call. ---
    # pgrep: "no Irrlicht app running" — stops the pkill branch being taken.
    printf '#!/bin/sh\nexit 1\n' >"$root/bin/pgrep"
    # pkill: must never reach a real process even if something calls it.
    printf '#!/bin/sh\nexit 0\n' >"$root/bin/pkill"
    # lsof: present (so install.sh takes the lsof branch, not the pgrep -x
    # fallback) but reports nothing listening on 7837.
    printf '#!/bin/sh\nexit 0\n' >"$root/bin/lsof"
    printf '#!/bin/sh\nexit 0\n' >"$root/bin/launchctl"
    printf '#!/bin/sh\nexit 0\n' >"$root/bin/systemctl"
    chmod +x "$root/bin/"*

    printf '%s' "$root"
}

# make_irrlichd <path> <marker> <exitcode> — a stand-in for the real binary.
#
# On `--uninstall-hooks` it does what the real registry-driven sweep does to the
# files this test can see: strips irrlicht's entries out of the two configs.
# Using a stub rather than a built binary keeps the test hermetic and free of a
# Go toolchain (test.yml runs the shell-lib step without one); the sweep's own
# correctness is covered by the Go tests around uninstallHookConfigs. What this
# test owns is the question the Go tests cannot answer: does install.sh ever
# CALL it, and does it call it while the binary still exists.
make_irrlichd() {
    local path="$1" marker="$2" code="${3:-0}"
    mkdir -p "$(dirname "$path")"
    cat >"$path" <<STUB
#!/bin/sh
# Record every invocation, with the argv, so the test can prove it ran and that
# it ran from a binary that had not been deleted yet.
printf '%s\n' "\$*" >>"$marker"
if [ "\$1" != "--uninstall-hooks" ]; then
    printf 'stub irrlichd: unexpected args: %s\n' "\$*" >&2
    exit 64
fi
if [ "$code" -ne 0 ]; then
    printf 'stub irrlichd: simulated sweep failure\n' >&2
    exit $code
fi
# Emulate the real sweep: remove the irrlicht entries.
printf '%s\n' '{ "hooks": {} }' >"\$HOME/.claude/settings.json"
printf '%s\n' '{ "hooks": {} }' >"\$HOME/.codex/hooks.json"
printf 'Removed irrlicht hooks from %s\n' "\$HOME/.claude/settings.json"
exit 0
STUB
    chmod +x "$path"
}

# run_uninstall <root> [extra-env...] — run the real installer's uninstall path
# inside the fake machine. Returns its exit code; echoes stdout+stderr.
run_uninstall() {
    local root="$1"; shift
    env -i \
        HOME="$root/home" \
        PATH="$root/bin:/usr/bin:/bin:/usr/sbin:/sbin" \
        IRRLICHT_APP_PATH="$root/app/Irrlicht.app" \
        "$@" \
        sh "$INSTALL_SH" --uninstall 2>&1
}

# ===========================================================================
# 1. THE DEFECT. A clean uninstall must leave no irrlicht hook entries behind.
#    This is the case that fails on main: nothing calls the sweep, so both
#    configs still name irrlichd after the uninstall reports success.
# ===========================================================================
root="$(new_env defect)"
make_irrlichd "$root/home/.local/bin/irrlichd" "$root/marker" 0
out="$(run_uninstall "$root")"; rc=$?

assert_eq "$rc" "0" "defect: uninstall exits 0"
assert_contains "$out" "Irrlicht uninstalled" "defect: uninstall reports success"
assert_not_contains "$(cat "$root/home/.claude/settings.json")" "irrlichd" \
    "defect: ~/.claude/settings.json has no irrlichd entry left"
assert_not_contains "$(cat "$root/home/.claude/settings.json")" "7837" \
    "defect: ~/.claude/settings.json has no irrlicht hook endpoint left"
assert_not_contains "$(cat "$root/home/.codex/hooks.json")" "irrlichd" \
    "defect: ~/.codex/hooks.json has no irrlichd entry left"

# ===========================================================================
# 2. ORDERING. The sweep needs the binary, and the uninstall deletes it. Both
#    must be true at the end: the stub ran (marker written) AND the binary is
#    gone — which can only happen if the call came first.
# ===========================================================================
assert_file_present "$root/marker" "ordering: irrlichd was invoked during the uninstall"
if [[ -f "$root/marker" ]]; then
    assert_eq "$(cat "$root/marker")" "--uninstall-hooks" \
        "ordering: invoked exactly once, with --uninstall-hooks and nothing else"
fi
assert_file_absent "$root/home/.local/bin/irrlichd" "ordering: the binary is still deleted afterwards"

# ===========================================================================
# 3. BINARY ALREADY GONE (partial earlier uninstall, hand-deleted binary).
#    Skip with a message; never fail the uninstall.
# ===========================================================================
root="$(new_env nobinary)"
out="$(run_uninstall "$root")"; rc=$?

assert_eq "$rc" "0" "no-binary: uninstall still exits 0"
assert_contains "$out" "Irrlicht uninstalled" "no-binary: uninstall still completes"
assert_contains "$out" "No irrlichd binary" "no-binary: says why the hook cleanup was skipped"
assert_file_absent "$root/marker" "no-binary: nothing was invoked"

# ===========================================================================
# 4. FAIL SOFT. A sweep that errors must not abort the uninstall — the user
#    asked for removal, and a half-removed product is worse than the residue.
# ===========================================================================
root="$(new_env sweepfails)"
make_irrlichd "$root/home/.local/bin/irrlichd" "$root/marker" 1
out="$(run_uninstall "$root")"; rc=$?

assert_eq "$rc" "0" "fail-soft: uninstall exits 0 despite the sweep failing"
assert_contains "$out" "Irrlicht uninstalled" "fail-soft: uninstall still completes"
assert_contains "$out" "hook" "fail-soft: the failure is surfaced, not swallowed"
assert_file_absent "$root/home/.local/bin/irrlichd" "fail-soft: the binary is still removed"

# ===========================================================================
# 5. APP-BUNDLE INSTALL. A full macOS install has no ~/.local/bin/irrlichd —
#    the daemon lives inside Irrlicht.app, which this same function deletes.
# ===========================================================================
root="$(new_env appbundle)"
make_irrlichd "$root/app/Irrlicht.app/Contents/MacOS/irrlichd" "$root/marker" 0
out="$(run_uninstall "$root")"; rc=$?

assert_eq "$rc" "0" "app-bundle: uninstall exits 0"
assert_file_present "$root/marker" "app-bundle: the in-bundle irrlichd was invoked"
assert_not_contains "$(cat "$root/home/.claude/settings.json")" "irrlichd" \
    "app-bundle: ~/.claude/settings.json comes back clean"
assert_file_absent "$root/app/Irrlicht.app" "app-bundle: the bundle is still removed afterwards"

# ===========================================================================
# 6. LINUX PATH. The systemd branch lives in the same function and has the same
#    exposure. Force the Linux branch with a `uname` stub and give it a unit
#    file to remove, then assert the hooks were swept there too.
# ===========================================================================
root="$(new_env linux)"
printf '#!/bin/sh\nif [ "${1:-}" = "-s" ]; then echo Linux; else echo Linux; fi\n' >"$root/bin/uname"
chmod +x "$root/bin/uname"
mkdir -p "$root/home/.config/systemd/user"
printf '[Unit]\n' >"$root/home/.config/systemd/user/irrlichd.service"
make_irrlichd "$root/home/.local/bin/irrlichd" "$root/marker" 0
out="$(run_uninstall "$root")"; rc=$?

assert_eq "$rc" "0" "linux: uninstall exits 0"
assert_file_present "$root/marker" "linux: irrlichd was invoked on the systemd path too"
assert_not_contains "$(cat "$root/home/.codex/hooks.json")" "irrlichd" \
    "linux: ~/.codex/hooks.json comes back clean"
assert_file_absent "$root/home/.config/systemd/user/irrlichd.service" \
    "linux: the systemd user unit is still removed"

# ===========================================================================
# 7. THE RE-INSTALL PATH MUST NOT SWEEP.
#
#    uninstall_previous() has two call sites: the real --uninstall, and the
#    upgrade path that clears the old install before laying down the new one.
#    --uninstall-hooks also records the hooks permissions as DENIED (#570,
#    denyHooksPermissions in core/cmd/irrlichd/main.go) so a persisted "granted"
#    cannot re-install them on the next daemon start. That denial lives in the
#    user data an uninstall deliberately KEEPS — so sweeping on the upgrade path
#    would silently switch hook-based monitoring off for a user who only asked
#    for a newer version, and leave them to rediscover the permission wizard.
#
#    This is asserted structurally rather than behaviourally: the upgrade path
#    only reaches uninstall_previous after a real download and checksum, which a
#    unit test has no way to satisfy.
# ===========================================================================
# The function's own definition line matches the same prefix — drop it, or the
# count is off by one and the assertion silently measures the wrong thing.
callsites="$(grep -n '^[[:space:]]*uninstall_previous' "$INSTALL_SH" | grep -v 'uninstall_previous()')"
sweeping="$(printf '%s\n' "$callsites" | grep -c 'uninstall_previous[[:space:]][[:space:]]*1' || true)"
total="$(printf '%s\n' "$callsites" | grep -c 'uninstall_previous' || true)"
assert_eq "$total" "2" "callsites: uninstall_previous still has exactly two call sites"
assert_eq "$sweeping" "1" "callsites: exactly one of them opts into the hook sweep"

# ===========================================================================
# 8. The installer must stay `sh`-clean — it runs under /bin/sh, not bash.
# ===========================================================================
if sh -n "$INSTALL_SH" 2>/dev/null; then
    pass "syntax: site/install.sh parses under /bin/sh"
else
    fail "syntax: site/install.sh parses under /bin/sh"
fi

if [ "$fails" -eq 0 ]; then
    echo "$NAME: ALL PASS"
else
    echo "$NAME: $fails FAILED" >&2
    exit 1
fi
