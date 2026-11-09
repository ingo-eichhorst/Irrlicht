#!/usr/bin/env bash
# release-keychain_test.sh — tests for tools/release-keychain.sh, run against a
# throwaway keychain in a temporary directory. The user's keychain search list
# is snapshotted first and compared at the end (`security create-keychain`
# does not add to it, measured on macOS 26), and the throwaway keychain is
# deleted on exit.
#
# Every assertion covers behaviour the tool ADDS, so none of them is a lock in
# AGENTS.md's sense. The terminal case is the one a plain unlock gets wrong,
# and its evidence is committed, not described: the same fixture runs a naive
# `security unlock-keychain <kc> < <file>` as a control and asserts that IT
# fails there. If macOS ever stopped preferring the terminal, the control
# would pass, and this file fails rather than keep a case that no longer
# discriminates.
#
# macOS only: keychains and BSD script(1) don't exist elsewhere. On another OS
# it prints NOT RUN and exits 0; test.yml runs this suite on macos-latest.

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
TOOL="$REPO_ROOT/tools/release-keychain.sh"
NAME="release-keychain_test"

if [[ "$(uname -s)" != "Darwin" ]]; then
    echo "NOT RUN: $NAME — keychains and BSD script(1) are macOS-only; this is $(uname -s)"
    exit 0
fi

# A missing tool is a failure, not a skip: exiting 0 would read as a pass.
for t in security python3 script mktemp openssl; do
    command -v "$t" >/dev/null 2>&1 || { echo "FAIL: $NAME — $t not found"; exit 1; }
done

fails=0
pass() { printf 'PASS: %s\n' "$1"; }
fail() {
    printf 'FAIL: %s\n' "$1"
    [[ $# -gt 1 ]] && printf '      %s\n' "$2"
    fails=$((fails + 1))
    return 0
}

TMP=$(mktemp -d -t release-keychain-test) || { echo "FAIL: $NAME — mktemp"; exit 1; }
KC="$TMP/throwaway.keychain-db"
SEARCH_BEFORE=$(security list-keychains -d user)
cleanup() {
    [[ -e "$KC" ]] && security delete-keychain "$KC" >/dev/null 2>&1
    rm -rf "$TMP"
}
trap cleanup EXIT

# The throwaway password goes on argv once, to create the keychain. It guards
# nothing.
PW=$(openssl rand -hex 16)
security create-keychain -p "$PW" "$KC" || { echo "FAIL: $NAME — cannot create a throwaway keychain"; exit 1; }
( umask 077; printf '%s\n' "$PW" > "$TMP/pw"; printf 'not-the-password\n' > "$TMP/wrong" )

state() { security show-keychain-info "$KC" >/dev/null 2>&1 && echo unlocked || echo locked; }
relock() { security lock-keychain "$KC"; [[ "$(state)" == locked ]] || { echo "FAIL: $NAME — cannot relock the throwaway keychain"; exit 1; }; }

# run_tool <args...> — sets OUT, ERR, RC.
run_tool() {
    OUT=$("$TOOL" "$@" 2>"$TMP/err"); RC=$?
    ERR=$(cat "$TMP/err")
}

# bounded <seconds> <cmd...> — runs cmd in the background, returns its status,
# or kills it (and anything left holding the temp dir) and returns 124.
bounded() {
    local secs="$1"; shift
    "$@" &
    local pid=$! i
    for (( i = 0; i < secs * 4; i++ )); do
        if ! kill -0 "$pid" 2>/dev/null; then
            wait "$pid"
            return $?
        fi
        sleep 0.25
    done
    kill "$pid" 2>/dev/null
    pkill -f "$TMP" 2>/dev/null
    wait "$pid" 2>/dev/null
    return 124
}

relock

echo "== unlock =="
run_tool unlock "$KC" "$TMP/pw"
if [[ "$RC" -eq 0 && "$OUT" == "unlocked" && "$(state)" == unlocked ]]; then
    pass "a locked keychain is unlocked from a mode-600 file and reported as 'unlocked'"
else
    fail "unlock from a mode-600 file" "rc=$RC out=[$OUT] state=$(state) err=[$ERR]"
fi

run_tool unlock "$KC" "$TMP/pw"
if [[ "$RC" -eq 0 && "$OUT" == "already-unlocked" ]]; then
    pass "an unlocked keychain is reported as 'already-unlocked', so the caller won't relock it"
else
    fail "unlock of an unlocked keychain" "rc=$RC out=[$OUT] err=[$ERR]"
fi

echo "== lock =="
run_tool lock "$KC"
if [[ "$RC" -eq 0 && "$(state)" == locked ]]; then
    pass "lock leaves the keychain locked"
else
    fail "lock" "rc=$RC state=$(state) err=[$ERR]"
fi
relock

echo "== refusals =="
cp "$TMP/pw" "$TMP/pw-644"; chmod 644 "$TMP/pw-644"
run_tool unlock "$KC" "$TMP/pw-644"
if [[ "$RC" -eq 1 && "$(state)" == locked && "$ERR" == *"group or other"* ]]; then
    pass "a password file other users can read is refused, and the keychain stays locked"
else
    fail "mode-644 password file" "rc=$RC state=$(state) err=[$ERR]"
fi
relock

ln -s "$TMP/pw" "$TMP/pw-link"
run_tool unlock "$KC" "$TMP/pw-link"
if [[ "$RC" -eq 1 && "$(state)" == locked && "$ERR" == *"symlink"* ]]; then
    pass "a symlinked password file is refused"
else
    fail "symlinked password file" "rc=$RC state=$(state) err=[$ERR]"
fi
relock

run_tool unlock "$KC" "$TMP/wrong"
if [[ "$RC" -eq 1 && "$(state)" == locked && "$ERR" == *"exited 51"* ]]; then
    pass "a wrong password fails loudly with security's exit 51, and the keychain stays locked"
else
    fail "wrong password" "rc=$RC state=$(state) err=[$ERR]"
fi
relock

run_tool unlock "$KC" "$TMP/missing"
if [[ "$RC" -eq 1 && "$ERR" == *"does not exist"* ]]; then
    pass "a missing password file is refused"
else
    fail "missing password file" "rc=$RC err=[$ERR]"
fi

run_tool unlock "$TMP/no.keychain-db" "$TMP/pw"
if [[ "$RC" -eq 1 && "$ERR" == *"does not exist"* ]]; then
    pass "a missing keychain is refused"
else
    fail "missing keychain" "rc=$RC err=[$ERR]"
fi

run_tool unlock "$KC"
if [[ "$RC" -eq 2 ]]; then
    pass "a usage error exits 2"
else
    fail "usage error" "rc=$RC err=[$ERR]"
fi

echo "== under a terminal =="
# script(1) gives the command a controlling terminal whose input never comes,
# the shape a maintainer's interactive run has. stdin is /dev/null.
relock
bounded 20 script -q /dev/null "$TOOL" unlock "$KC" "$TMP/pw" </dev/null >/dev/null 2>&1
rc=$?
if [[ "$rc" -eq 0 && "$(state)" == unlocked ]]; then
    pass "unlock works with a controlling terminal attached, without waiting for typing"
else
    fail "unlock under a terminal" "rc=$rc state=$(state) (124 = killed after 20s)"
fi

relock
bounded 5 script -q /dev/null sh -c 'security unlock-keychain "$1" < "$2"' sh "$KC" "$TMP/pw" </dev/null >/dev/null 2>&1
rc=$?
if [[ "$(state)" == locked ]]; then
    pass "control: a naive 'security unlock-keychain < file' under the same terminal does NOT unlock (rc=$rc), so the case above discriminates"
else
    fail "control: the naive unlock worked under a terminal" \
         "security now reads stdin even with a terminal; the terminal case no longer tests anything, so revisit tools/release-keychain.sh's detached unlock"
fi
relock

if [[ "$(security list-keychains -d user)" != "$SEARCH_BEFORE" ]]; then
    fail "the user's keychain search list changed during the test" "before: $SEARCH_BEFORE"
fi

if [[ "$fails" -gt 0 ]]; then
    echo "$NAME: $fails failure(s)"
    exit 1
fi
echo "$NAME: all passed"
