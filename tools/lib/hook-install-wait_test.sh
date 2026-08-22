#!/usr/bin/env bash
# hook-install-wait_test.sh — unit tests for
# tools/onboarding-factory/scripts/lib/hook-install-wait.sh. Plain bash, no
# framework, matching tools/lib/await-gone_test.sh. Run directly, or via
# tools/preflight.sh's `tools` gate and test.yml's tools/lib loop.
#
# WHAT IS GRADED. The library had no test corpus at all; this file adds one for
# the behaviour #1770 ADDS — HOOK_INSTALL_WAIT_PATHS, the operator-named path
# for the two hooks-declaring adapters that structurally cannot have a rig-home
# row — and pins, as LOCKS, the two pre-existing "did not check anything" arms
# it must not have changed.
#
# The added rows each owe a mutation seen red, and each was run against this
# file before the arm existed:
#
#   - delete the `hook_install_manifest_has` guard → "refuses a path the daemon
#     does not declare" goes red (it returns 0 and polls a file nothing writes).
#   - accept a relative path → "refuses a relative path" goes red.
#   - poll the prefix instead of the explicit list → "waits for an
#     operator-named path" goes red, because the manifest here declares the
#     path under no exported home.
#   - return 0 on deadline → "fails when the named path never arrives" goes red.
#
# The two LOCKS pass by construction and are labelled as such rather than
# presented as red-first evidence: they are the arms that predate this change.
#
# Time is bought down by the poll knobs, which the library reads at call time
# for exactly this reason. Nothing here sleeps for a fixed duration to "let
# something happen" — the one row that must observe an arrival writes the file
# first, so the poll's own loop is the observation.

set -uo pipefail   # NOT -e: assertions capture non-zero return codes

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
LIBDIR="$REPO_ROOT/tools/onboarding-factory/scripts/lib"

for f in agent-home.sh hook-install-wait.sh; do
  if [[ ! -f "$LIBDIR/$f" ]]; then
    echo "FAIL: hook-install-wait_test — subject not found at $LIBDIR/$f" >&2
    exit 1
  fi
done
# agent-home.sh first: wait_for_hook_install calls agent_home_var from it, and a
# missing function would make every row take the "no isolated home" arm and
# pass for the wrong reason.
# shellcheck source=../onboarding-factory/scripts/lib/agent-home.sh
source "$LIBDIR/agent-home.sh"
# shellcheck source=../onboarding-factory/scripts/lib/hook-install-wait.sh
source "$LIBDIR/hook-install-wait.sh"
for fn in agent_home_var wait_for_hook_install hook_install_explicit_paths hook_install_manifest_has; do
  if ! declare -F "$fn" >/dev/null; then
    echo "FAIL: hook-install-wait_test — sourcing the libs defined no $fn" >&2
    exit 1
  fi
done

# Fast poll: 10 × 0.05s = 0.5s to the deadline. These are the knobs the library
# documents as existing so the tests need not spend production seconds.
#
# shellcheck disable=SC2034  # read by the SOURCED library, not by this file:
# hook-install-wait.sh defaults both at its "Poll knobs" block and reads them in
# wait_for_hook_install's loop. shellcheck does not follow a source through a
# variable path, so it cannot see the consumer.
HOOK_INSTALL_WAIT_TICKS=10
# shellcheck disable=SC2034  # same reader as the tick count above.
HOOK_INSTALL_WAIT_TICK_S=0.05

TMP="$(mktemp -d -t hookinstallwait)"
trap 'rm -rf "$TMP"' EXIT

fails=0
pass() { echo "  PASS: $1"; return 0; }
fail() { echo "  FAIL: $1 — expected [$2] got [$3]"; fails=$((fails + 1)); return 0; }
assert_eq() {
  if [[ "$2" == "$3" ]]; then pass "$1"; else fail "$1" "$2" "$3"; fi
}
# A refusal assertion names a FRAGMENT of the message, never just the status:
# several arms return 1, so "it returned 1" is satisfied by the wrong refusal.
assert_says() {
  case "$3" in
    *"$2"*) pass "$1" ;;
    *) fail "$1" "output containing: $2" "${3:-nothing}" ;;
  esac
}

# staging_with <name> <manifest-lines...> builds a staging tree carrying a
# managed-file manifest, in the tab-separated shape managed-file-snapshot.sh
# writes: <state>\t<slot>\t<path>.
staging_with() {
  local name="$1"; shift
  local s="$TMP/$name"
  mkdir -p "$s/managed-file-backup"
  : > "$s/managed-file-backup/manifest"
  local line
  for line in "$@"; do
    printf '%s\n' "$line" >> "$s/managed-file-backup/manifest"
  done
  printf '%s\n' "$s"
}

echo "== HOOK_INSTALL_WAIT_PATHS =="

PLUGIN="$TMP/xdg/opencode/plugin/irrlicht.js"
S1="$(staging_with s1 "$(printf 'absent\t0\t%s' "$PLUGIN")")"

# The file is written BEFORE the call, so what is asserted is that the poll
# looked at the operator's path and saw it — not that a sleep was long enough.
mkdir -p "$(dirname "$PLUGIN")"; : > "$PLUGIN"
out="$(HOOK_INSTALL_WAIT_PATHS="$PLUGIN" wait_for_hook_install opencode "$S1" 2>&1)"; rc=$?
assert_eq "waits for an operator-named path and returns 0 when it is there" 0 "$rc"
assert_says "…and says which subject it checked" "1 operator-named path" "$out"

rm -f "$PLUGIN"
out="$(HOOK_INSTALL_WAIT_PATHS="$PLUGIN" wait_for_hook_install opencode "$S1" 2>&1)"; rc=$?
assert_eq "fails when the named path never arrives" 1 "$rc"
assert_says "…and names the file that never appeared" "$PLUGIN" "$out"
assert_says "…and says it is refusing to drive the adapter" "refusing to drive opencode" "$out"

# The vacuity guard. A path the daemon does not manage can never arrive, so
# polling it is a wait that hangs and then reports a failure whose real cause
# (a home variable exported after the daemon spawned) is nowhere on screen.
out="$(HOOK_INSTALL_WAIT_PATHS="$TMP/nowhere/irrlicht.js" wait_for_hook_install opencode "$S1" 2>&1)"; rc=$?
assert_eq "refuses a path the daemon does not declare" 1 "$rc"
assert_says "…and says the daemon does not declare it" "does not declare" "$out"

out="$(HOOK_INSTALL_WAIT_PATHS="relative/irrlicht.js" wait_for_hook_install opencode "$S1" 2>&1)"; rc=$?
assert_eq "refuses a relative path" 1 "$rc"
assert_says "…and says it must be absolute" "must name absolute paths" "$out"

# Two paths, colon-separated: both must be present, so one missing is a refusal
# even though the other arrived.
OTHER="$TMP/xdg/opencode/plugin/second.js"
S2="$(staging_with s2 \
  "$(printf 'absent\t0\t%s' "$PLUGIN")" \
  "$(printf 'absent\t1\t%s' "$OTHER")")"
mkdir -p "$(dirname "$PLUGIN")"; : > "$PLUGIN"
out="$(HOOK_INSTALL_WAIT_PATHS="$PLUGIN:$OTHER" wait_for_hook_install opencode "$S2" 2>&1)"; rc=$?
assert_eq "a colon-separated list needs EVERY path present" 1 "$rc"
assert_says "…and names only the missing one" "$OTHER" "$out"
: > "$OTHER"
out="$(HOOK_INSTALL_WAIT_PATHS="$PLUGIN:$OTHER" wait_for_hook_install opencode "$S2" 2>&1)"; rc=$?
assert_eq "…and passes once both are present" 0 "$rc"
assert_says "…counting both" "2 operator-named path" "$out"

echo "== locks (behaviour that predates this change; these pass by construction) =="

# LOCK: an adapter with no exported home and no operator-named path still
# declines to wait, and still SAYS it declined. Silence here would be
# indistinguishable from a check that ran.
S3="$(staging_with s3 "$(printf 'absent\t0\t%s' "$TMP/elsewhere/x.json")")"
out="$(wait_for_hook_install opencode "$S3" 2>&1)"; rc=$?
assert_eq "LOCK: no home, no named path → returns 0" 0 "$rc"
assert_says "LOCK: …and says it is NOT waiting" "NOT waiting" "$out"

# LOCK: an exported home under which the daemon declares nothing is reported as
# "nothing was checked", never as a passing check.
S4="$(staging_with s4 "$(printf 'absent\t0\t%s' "$TMP/elsewhere/x.json")")"
export PI_CODING_AGENT_DIR="$TMP/pi-home"
out="$(wait_for_hook_install pi "$S4" 2>&1)"; rc=$?
unset PI_CODING_AGENT_DIR
assert_eq "LOCK: home exported but nothing declared under it → returns 0" 0 "$rc"
assert_says "LOCK: …and says nothing was checked" "nothing was checked" "$out"

# LOCK: the rig-home path still works end to end for an adapter that has a row.
S5DIR="$TMP/pi-home2"
S5="$(staging_with s5 "$(printf 'absent\t0\t%s' "$S5DIR/extensions/irrlicht.js")")"
mkdir -p "$S5DIR/extensions"; : > "$S5DIR/extensions/irrlicht.js"
export PI_CODING_AGENT_DIR="$S5DIR"
out="$(wait_for_hook_install pi "$S5" 2>&1)"; rc=$?
unset PI_CODING_AGENT_DIR
assert_eq "LOCK: rig-home prefix selection still passes when the file is there" 0 "$rc"
assert_says "LOCK: …naming the prefix it selected on" "$S5DIR" "$out"

if (( fails > 0 )); then
  echo "hook-install-wait_test: $fails failure(s)"
  exit 1
fi
echo "hook-install-wait_test: all assertions passed"
