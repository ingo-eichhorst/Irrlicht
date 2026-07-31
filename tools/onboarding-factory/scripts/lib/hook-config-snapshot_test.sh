#!/usr/bin/env bash
# hook-config-snapshot_test.sh — unit tests for hook-config-snapshot.sh. Plain
# bash, no framework. Run directly or via scripts/smoke-test.sh.
#
# The lib is sourced (not run as a subprocess) so the tests drive its two
# functions directly against a fake $HOME / $CODEX_HOME. What matters is that a
# recording daemon's edits to the user's shared agent config never survive the
# run — including the case where the daemon created a config file that was not
# there before (#1178).

set -uo pipefail   # NOT -e: assertions capture non-zero return codes

DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=lib/hook-config-snapshot.sh
source "$DIR/hook-config-snapshot.sh"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# Floor under every case: point $HOME and $CODEX_HOME somewhere harmless before
# the first one runs. fresh_env overrides both per-case, so this changes nothing
# about what the tests exercise — it exists so a case that is added above the
# first fresh_env call, or forgets it, cannot reach the developer's real
# ~/.claude/settings.json. That is not hypothetical: a draft of this file did
# exactly that and wiped a live config's statusLine and all seven irrlicht hook
# entries (#1212). These paths are deliberately NOT created — an omitted
# fresh_env then fails loudly on a missing directory instead of silently
# writing somewhere plausible.
HOME="$TMP/nohome"
CODEX_HOME="$TMP/nocodex"

fails=0
pass() { echo "  PASS: $1"; return 0; }
fail() { echo "  FAIL: $1 — expected [$2] got [$3]"; fails=$((fails + 1)); return 0; }
assert_eq() {
  [[ "$2" == "$3" ]] && pass "$1" || fail "$1" "$2" "$3"
}

# fresh_env points $HOME and $CODEX_HOME at a clean subdir, sets $ROOT, and
# clears the lib's state so each case starts from nothing. It must NOT be
# called in a command substitution — the env assignments have to land in this
# shell, not a subshell.
fresh_env() {
  ROOT="$TMP/$1"
  mkdir -p "$ROOT/home/.claude" "$ROOT/codex"
  HOME="$ROOT/home"
  CODEX_HOME="$ROOT/codex"
  HOOK_CONFIG_FILES=()
  HOOK_CONFIG_BACKUP_DIR=""
}

echo "== an existing config is restored byte-for-byte =="
fresh_env existing
printf '{"hooks":{"Stop":"localhost:7837"}}\n' > "$HOME/.claude/settings.json"
snapshot_hook_configs "$ROOT/backup"
printf '{"hooks":{"Stop":"localhost:7838"}}\n' > "$HOME/.claude/settings.json"   # daemon repoints it
restore_hook_configs
assert_eq "settings.json restored" '{"hooks":{"Stop":"localhost:7837"}}' "$(cat "$HOME/.claude/settings.json")"

echo "== a config the daemon created from nothing is removed =="
fresh_env created
snapshot_hook_configs "$ROOT/backup"
printf '{"hooks":{}}\n' > "$CODEX_HOME/hooks.json"                               # daemon creates it
restore_hook_configs
[[ -e "$CODEX_HOME/hooks.json" ]] && got=present || got=absent
assert_eq "hooks.json removed" "absent" "$got"

echo "== an untouched config survives a restore unchanged =="
fresh_env untouched
printf 'original\n' > "$CODEX_HOME/hooks.json"
snapshot_hook_configs "$ROOT/backup"
restore_hook_configs
assert_eq "hooks.json unchanged" "original" "$(cat "$CODEX_HOME/hooks.json")"

echo "== restore without a snapshot is a no-op, not an error =="
fresh_env nosnapshot
printf 'untouched\n' > "$HOME/.claude/settings.json"
restore_hook_configs
rc=$?
assert_eq "restore returns 0" "0" "$rc"
assert_eq "file left alone" "untouched" "$(cat "$HOME/.claude/settings.json")"

echo ""
if [[ "$fails" -eq 0 ]]; then
  echo "hook-config-snapshot_test: ALL PASS"
  exit 0
fi
echo "hook-config-snapshot_test: $fails FAILURE(S)" >&2
exit 1
