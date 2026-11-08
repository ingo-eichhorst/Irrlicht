#!/usr/bin/env bash
# run-cell-multi-opencode-v2_test.sh — what the cross-adapter rig
# (run-cell-multi.sh) learned for the opencode-v2 column (#2109 P5b-1, cell
# 4-2). Plain bash, no framework, and no tmux: the recording host has none, so
# nothing here may need one. Run directly or via smoke-test.sh.
#
# run-cell.sh learned three things for the column in P5a; this rig had none of
# them (P5a's PR body lists the two below as left for P5b-1):
#
#   * the opencode-v2 store wipe — the column records into the OpenCode
#     service's own shared IRRLICHT_HOME, so a session an earlier daemon
#     persisted there would be restored into the recording. The rig must ask
#     spawn_record_daemon to wipe (RECORD_DAEMON_WIPE_STATE_STORES) when either
#     adapter of the pair is opencode-v2, and only then — the other columns'
#     behaviour is a lock here;
#   * the tmux-teardown exemption for a driver that launches no tmux —
#     graded by lib/run-cell-multi-teardown_test.sh, which owns that gate; this
#     file checks only that the launch loop hands the gate the driver;
#   * resolution of the column's 4-2 cell: `--dry-run` resolves the pair from
#     the committed catalog and lints both recipes, with no daemon and no CLI.
#
# HOW: run-cell-multi.sh is a top-to-bottom script, so the decision is the
# `# BEGIN multi_wipes_state_stores` block, extracted from the script's source
# text and eval'd — the extract-and-execute idiom run-cell-multi-teardown_test.sh
# uses. An extraction that comes back empty refuses rather than grading
# nothing. The mutation for the wipe decision lives in
# tools/lib/opencode-v2-rig-mutations_test.sh (25).

set -uo pipefail   # NOT -e: assertions capture non-zero return codes

DIR="$(cd "$(dirname "$0")" && pwd)"
SCRIPTS_DIR="$(cd "$DIR/.." && pwd)"
REPO_ROOT="$(cd "$SCRIPTS_DIR/../../.." && pwd)"
RUN_CELL_MULTI="$SCRIPTS_DIR/run-cell-multi.sh"

command -v jq >/dev/null || { echo "run-cell-multi-opencode-v2_test: jq is required" >&2; exit 2; }

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

fails=0
pass() { echo "  PASS: $1"; return 0; }
fail() { echo "  FAIL: $1 — expected [$2] got [$3]"; fails=$((fails + 1)); return 0; }
assert_eq() { if [[ "$2" == "$3" ]]; then pass "$1"; else fail "$1" "$2" "$3"; fi; return 0; }
assert_contains() { case "$3" in *"$2"*) pass "$1" ;; *) fail "$1" "*$2*" "${3:0:600}" ;; esac; return 0; }

src="$(awk '
  /^# BEGIN multi_wipes_state_stores([ \t]|$)/ { inb = 1; next }
  inb && /^# END multi_wipes_state_stores[ \t]*$/ { inb = 0; next }
  inb { print }
' "$RUN_CELL_MULTI")"
if [[ -z "$src" ]] || ! grep -q '^multi_wipes_state_stores() {' <<<"$src"; then
  echo "run-cell-multi-opencode-v2_test: REFUSING — no '# BEGIN multi_wipes_state_stores' block defining" >&2
  echo "  multi_wipes_state_stores() in $RUN_CELL_MULTI; every case below would grade nothing." >&2
  exit 1
fi
eval "$src" || { echo "run-cell-multi-opencode-v2_test: REFUSING — the block does not eval" >&2; exit 1; }

wipes() { if multi_wipes_state_stores "$@"; then echo wipe; else echo keep; fi; }

echo "== the opencode-v2 store wipe is asked for exactly when the pair includes the column =="
assert_eq "opencode-v2 + claudecode: wipe" "wipe" "$(wipes opencode-v2 claudecode)"
assert_eq "claudecode + opencode-v2 (either position): wipe" "wipe" "$(wipes claudecode opencode-v2)"
# Locks: the other columns' behaviour does not change.
assert_eq "the V1 opencode column + claudecode: keep (its home is the rig's own)" "keep" "$(wipes opencode claudecode)"
assert_eq "claudecode + codex: keep" "keep" "$(wipes claudecode codex)"

echo "== both are WIRED into the script's control flow =="
wired() { grep -qE "$2" "$1" && echo yes || echo no; }
assert_eq "the decision sets RECORD_DAEMON_WIPE_STATE_STORES before the spawn" "yes" \
  "$(awk '/^if multi_wipes_state_stores "\$\{ADAPTERS\[@\]\}"; then$/ { inif = 1; next }
          inif && /^[[:space:]]*RECORD_DAEMON_WIPE_STATE_STORES=1$/ { set = 1 }
          inif && /^fi$/ { inif = 0 }
          set && /^spawn_record_daemon / { print "yes"; exit }' "$RUN_CELL_MULTI")"
assert_eq "the teardown gate is handed each adapter's driver" "yes" \
  "$(wired "$RUN_CELL_MULTI" '^[[:space:]]*record_driver_teardown "\$\{DRV_ADAPTERS\[\$i\]\}" "\$\{DRV_PIDS\[\$i\]\}" "\$\{DRV_TIMEOUTS\[\$i\]\}" "\$\{DRV_DRIVERS\[\$i\]\}"$')"
assert_eq "…and DRV_DRIVERS is filled by the launch loop" "yes" \
  "$(wired "$RUN_CELL_MULTI" '^[[:space:]]*DRV_DRIVERS\+=\("\$driver"\)$')"

echo "== --dry-run resolves the column's 4-2 cell with no daemon, CLI or tmux =="
out="$(cd "$REPO_ROOT" && IRRLICHT_ONBOARD_HOME="$TMP/home/.local/share/irrlicht" \
  bash "$RUN_CELL_MULTI" --dry-run multiple-agents-same-workspace opencode-v2 2>&1)"; rc=$?
assert_eq "exit 0" "0" "$rc"
assert_contains "the pair is the column plus its recipe's partner" "adapters=[opencode-v2 claudecode]" "$out"
assert_contains "the column's own driver is the one it would launch" "driver: $REPO_ROOT/replaydata/agents/opencode-v2/driver-interactive.sh" "$out"
assert_contains "…in its own cell folder" "adapter: opencode-v2  folder: 4-2_multiple-agents-same-workspace" "$out"

echo ""
if (( fails == 0 )); then
  echo "run-cell-multi-opencode-v2_test: ALL PASS"
  exit 0
fi
echo "run-cell-multi-opencode-v2_test: $fails FAILED" >&2
exit 1
