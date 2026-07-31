#!/usr/bin/env bash
# hook-config-snapshot.sh — save and restore the agent config files a recording
# daemon rewrites, so a recording never outlives its own edits.
#
# A recording daemon runs with IRRLICHT_PERMISSION_MODE=grant-all and installs
# its hooks into the user's REAL agent config — ~/.claude/settings.json and
# $CODEX_HOME/hooks.json follow $HOME, not IRRLICHT_HOME, so
# IRRLICHT_ONBOARD_HOME does not isolate them. Since #1178 the endpoint written
# there carries the daemon's own bind port, so a coexist recording on :7838
# repoints a running production daemon's hooks at the recorder and leaves them
# that way. grant-all can also install entries the user had explicitly denied.
#
# Usage — call the first before spawning the daemon, the second from cleanup:
#   source "$SCRIPT_DIR/lib/hook-config-snapshot.sh"
#   snapshot_hook_configs "$STAGING/hook-config-backup"
#   ... spawn daemon, drive agent ...
#   restore_hook_configs
#
# Both are no-ops if the snapshot never ran, so restore is safe to call
# unconditionally from an EXIT trap.

# HOOK_CONFIG_FILES is the set of shared agent config files a recording daemon
# may rewrite. Set by snapshot_hook_configs and read by restore_hook_configs.
HOOK_CONFIG_FILES=()
HOOK_CONFIG_BACKUP_DIR=""

# snapshot_hook_configs <backup-dir> copies each shared agent config aside.
# A file that does not exist is recorded as absent (no backup written), so
# restore removes one the daemon created from nothing.
snapshot_hook_configs() {
  HOOK_CONFIG_BACKUP_DIR="$1"
  HOOK_CONFIG_FILES=("$HOME/.claude/settings.json" "${CODEX_HOME:-$HOME/.codex}/hooks.json")
  mkdir -p "$HOOK_CONFIG_BACKUP_DIR"
  local i
  for i in "${!HOOK_CONFIG_FILES[@]}"; do
    if [[ -f "${HOOK_CONFIG_FILES[$i]}" ]]; then
      cp "${HOOK_CONFIG_FILES[$i]}" "$HOOK_CONFIG_BACKUP_DIR/$i"
    fi
  done
  return 0
}

# restore_hook_configs puts each snapshotted file back exactly as it was.
# No backup for an index means the file did not exist when we snapshotted, so
# whatever is there now is ours to remove.
restore_hook_configs() {
  [[ -n "$HOOK_CONFIG_BACKUP_DIR" ]] || return 0
  local i
  for i in "${!HOOK_CONFIG_FILES[@]}"; do
    if [[ -f "$HOOK_CONFIG_BACKUP_DIR/$i" ]]; then
      cp "$HOOK_CONFIG_BACKUP_DIR/$i" "${HOOK_CONFIG_FILES[$i]}"
    else
      rm -f "${HOOK_CONFIG_FILES[$i]}"
    fi
  done
}
