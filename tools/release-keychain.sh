#!/usr/bin/env bash
# release-keychain.sh — unlock the release keychain from a password file, or
# lock it again, with nobody at the keyboard.
#
#   tools/release-keychain.sh unlock <keychain> <password-file>
#   tools/release-keychain.sh lock   <keychain>
#
# unlock prints one word on stdout and exits 0 once the keychain is unlocked:
# `unlocked` when this run unlocked it, `already-unlocked` when it already was.
# tools/build-release.sh relocks on exit only after `unlocked`, so it never
# locks a keychain somebody else opened. Any refusal or failure exits 1 with
# the reason on stderr; a usage error exits 2. lock exits 0 once the keychain
# is locked.
#
# Why this exists. A keychain is unlocked per login session: one unlocked in
# an `ssh -t factory` window still reads as locked from a release agent's
# session (measured 2026-11-09; see the ir:release skill, Step 6). Unlocking
# in the agent's own session took a human typing the password into a FIFO.
# With RELEASE_KEYCHAIN_PASSWORD_FILE set, build-release.sh calls this
# instead, so a release on the factory host needs nobody at all.
#
# The password file is a plaintext copy of the secret that guards the Developer
# ID private key, so the file is refused unless it is a regular file (not a
# symlink), owned by the invoking user, and has no group or other permission
# bits. Even then, anything that runs as that user can sign with the identity.
# That is the same exposure the Sparkle key file next to it already has. The
# password never goes on a command line (`security unlock-keychain -p` would
# put it in every `ps` listing) or into the environment.
#
# How it unlocks. Measured on macOS 26 with a throwaway keychain:
#   · Without a controlling terminal, `security unlock-keychain <kc>` reads
#     the password from stdin.
#   · WITH one, it reads the terminal and ignores stdin: under script(1), with
#     the password file on stdin, it waited for typing until it was killed.
#     A maintainer running a release by hand has a terminal.
#   · So `security` runs in a child that first leaves the terminal's session
#     with setsid(2), with the password file as its stdin. The child must fork
#     before setsid: a process that already leads a session gets EPERM, and
#     under script(1) the process does. macOS has no setsid(1), so python3,
#     which the Command Line Tools ship, does the fork, setsid and exec.
#   · A wrong password exits 51 without waiting.
# tools/lib/release-keychain_test.sh exercises each of these on a throwaway
# keychain, including the terminal case and a naive unlock as its control.

set -uo pipefail

usage() {
    echo "usage: $0 unlock <keychain> <password-file> | lock <keychain>" >&2
    exit 2
}

die() {
    echo "release-keychain: $*" >&2
    exit 1
}

is_unlocked() {
    security show-keychain-info "$1" >/dev/null 2>&1
}

# check_password_file <file> — refuses (exit 1) a file whose permissions would
# hand the password to anyone but its owner.
check_password_file() {
    local f="$1" owner mode
    [[ -L "$f" ]] && die "refusing $f: it is a symlink; point RELEASE_KEYCHAIN_PASSWORD_FILE at the file itself"
    [[ -f "$f" ]] || die "password file $f does not exist or is not a regular file"
    owner=$(stat -f '%u' "$f") || die "cannot stat $f"
    [[ "$owner" == "$(id -u)" ]] || die "refusing $f: owned by uid $owner, not by you ($(id -u))"
    mode=$(stat -f '%Lp' "$f") || die "cannot stat $f"
    if (( 8#$mode & 8#077 )); then
        die "refusing $f: mode $mode gives group or other access; run: chmod 600 '$f'"
    fi
}

# detached_unlock <keychain> <password-file> — runs `security unlock-keychain`
# with no controlling terminal and the file as stdin; returns its exit status.
detached_unlock() {
    python3 - "$1" "$2" <<'PY'
import os, sys

keychain, password_file = sys.argv[1], sys.argv[2]
pid = os.fork()
if pid == 0:
    try:
        os.setsid()
        fd = os.open(password_file, os.O_RDONLY)
        os.dup2(fd, 0)
        os.close(fd)
        devnull = os.open(os.devnull, os.O_WRONLY)
        os.dup2(devnull, 1)
        os.dup2(devnull, 2)
        os.execvp("security", ["security", "unlock-keychain", keychain])
    finally:
        os._exit(127)
_, status = os.waitpid(pid, 0)
sys.exit(os.waitstatus_to_exitcode(status))
PY
}

cmd_unlock() {
    local kc="$1" file="$2" rc=0
    [[ -f "$kc" ]] || die "keychain $kc does not exist"
    check_password_file "$file"
    if is_unlocked "$kc"; then
        echo "already-unlocked"
        return 0
    fi
    command -v python3 >/dev/null 2>&1 || die "python3 not found; it runs the detached unlock (macOS has no setsid(1))"
    detached_unlock "$kc" "$file" || rc=$?
    if [[ "$rc" -ne 0 ]]; then
        die "security unlock-keychain exited $rc for $kc; is $file this keychain's password? (51 means a wrong password)"
    fi
    is_unlocked "$kc" || die "security unlock-keychain exited 0, but $kc still reads as locked"
    echo "unlocked"
}

cmd_lock() {
    local kc="$1"
    [[ -f "$kc" ]] || die "keychain $kc does not exist"
    security lock-keychain "$kc" || die "security lock-keychain failed for $kc"
    if is_unlocked "$kc"; then
        die "security lock-keychain returned 0, but $kc still reads as unlocked"
    fi
}

[[ "$(uname -s)" == "Darwin" ]] || die "keychains are macOS-only; this is $(uname -s)"
case "${1:-}" in
    unlock) [[ $# -eq 3 ]] || usage; cmd_unlock "$2" "$3" ;;
    lock)   [[ $# -eq 2 ]] || usage; cmd_lock "$2" ;;
    *)      usage ;;
esac
