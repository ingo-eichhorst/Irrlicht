#!/usr/bin/env bash
# driver-launches-tmux_test.sh — unit tests for tmux-teardown-check.sh's
# driver_launches_tmux, the rule run-cell.sh's post-run tmux gate uses to skip
# a driver that never invokes tmux (#2109: the opencode-v2 API driver, on a
# host with no tmux). Plain bash. Run directly or via scripts/smoke-test.sh.
#
# The rule must answer "launches" for every way a driver can start a session —
# a spelling it missed would lose the run-time gate for a real tmux driver —
# and "launches nothing" only for a driver with no tmux command at all.

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$DIR/../../../.." && pwd)"
# shellcheck source=tmux-teardown-check.sh
source "$DIR/tmux-teardown-check.sh"

fails=0
assert_eq() {
  if [[ "$3" == "$2" ]]; then echo "  PASS: $1"; else echo "  FAIL: $1 — expected [$2] got [$3]"; fails=$((fails + 1)); fi
  return 0
}

T="$(mktemp -d)"
trap 'rm -rf "$T"' EXIT
printf '#!/usr/bin/env bash\n# not a launch: tmux new-session in a comment\ncurl -s http://127.0.0.1:1/api/session\n' > "$T/api-driver.sh"
printf '#!/usr/bin/env bash\ntmux new-session -d -s "x-$$" opencode\n' > "$T/tmux-driver.sh"
printf '#!/usr/bin/env bash\ntmux new -d -s "x-$$" opencode\n' > "$T/alias-driver.sh"
printf '#!/usr/bin/env bash\ntmux -L rig new-session -d -s "x-$$" opencode\n' > "$T/flag-driver.sh"

echo "== which drivers invoke tmux =="
driver_launches_tmux "$T/api-driver.sh"; assert_eq "an API driver launches no tmux" "1" "$?"
driver_launches_tmux "$T/tmux-driver.sh"; assert_eq "a tmux driver launches tmux" "0" "$?"
driver_launches_tmux "$T/missing.sh"; assert_eq "an unreadable driver is still graded" "0" "$?"
driver_launches_tmux "$T/alias-driver.sh"; assert_eq "the 'tmux new' alias still counts as a launch" "0" "$?"
driver_launches_tmux "$T/flag-driver.sh"; assert_eq "a global flag before the verb still counts" "0" "$?"
driver_launches_tmux "$ROOT/replaydata/agents/opencode-v2/driver-interactive.sh"
assert_eq "the opencode-v2 driver launches no tmux" "1" "$?"
driver_launches_tmux "$ROOT/replaydata/agents/opencode/driver-interactive.sh"
assert_eq "the opencode (1.x) driver still does" "0" "$?"

echo ""
if [[ "$fails" -eq 0 ]]; then
  echo "driver-launches-tmux_test: ALL PASS"
  exit 0
fi
echo "driver-launches-tmux_test: $fails FAILURE(S)" >&2
exit 1
