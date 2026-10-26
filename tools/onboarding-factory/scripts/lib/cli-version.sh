#!/usr/bin/env bash
# cli-version.sh — the one place the rig turns an adapter's CLI-version table
# arm into a version string. precheck.sh and promote-recording.sh each carry the
# table (adapter-tables_test.sh keeps them equal); both resolve an arm here.
#
# An arm is `<adapter>) CLI_BIN="<bin>"; VER_FIELD=<n> ;;`. Two shapes:
#
#   a binary  — run `<bin> --version` and take awk field <n> of its first line.
#   "@store"  — the sentinel CLI_VERSION_FROM_STORE: the version is read
#               PASSIVELY from the agent's own store, and nothing is executed.
#
# The opencode-v2 column (#2109) is the "@store" case, for a reason that is a
# hazard rather than a preference: on this rig's host `opencode` resolves to
# brew's 1.x (the wrong answer), and the 2.x binary, run without the sandbox
# environment, migrates the operator's real ~/.local/share/opencode/opencode.db
# in place, after which brew's 1.x hangs on it. So the column's version comes
# from the same row the daemon's own version gate reads
# (core/adapters/inbound/agents/opencode/storeversion.go,
# newestV2SessionVersionQuery): the newest session_v2.version.
# cli-version_test.sh proves no binary runs for it.
#
# Usage:
#   source "$SCRIPT_DIR/lib/cli-version.sh"
#   ver="$(resolve_cli_version "$ADAPTER" "$CLI_BIN" "$VER_FIELD")" || <could not read>

CLI_VERSION_FROM_STORE="@store"

# resolve_cli_version <adapter> <cli-bin> <ver-field> prints the version, with
# a trailing period/comma and a leading "v" stripped (copilot prints
# "… 1.0.77.", hermes "v0.19.0"). Exit 1, with the reason on stderr, when there
# is no version to print.
resolve_cli_version() {
  local adapter="$1" bin="$2" field="$3" ver
  if [[ "$bin" == "$CLI_VERSION_FROM_STORE" ]]; then
    ver="$(cli_version_from_store "$adapter")" || return 1
  else
    if ! command -v "$bin" >/dev/null 2>&1; then
      echo "cli-version: $bin CLI not on PATH" >&2
      return 1
    fi
    # Merge stderr — `pi --version` writes to stderr, others to stdout.
    ver="$("$bin" --version 2>&1 | awk -v f="$field" '{print $f}' | head -n1 | sed 's/[.,]$//')"
  fi
  ver="${ver#v}"
  if [[ -z "$ver" ]]; then
    echo "cli-version: could not parse a version for $adapter (from ${bin})" >&2
    return 1
  fi
  printf '%s\n' "$ver"
}

# cli_version_from_store <adapter> reads the version an "@store" adapter's own
# store records. Read-only: sqlite3 -readonly, no write, no binary.
cli_version_from_store() {
  case "$1" in
    opencode-v2) opencode_v2_store_version ;;
    *)
      echo "cli-version: $1 declares no store to read a version from" >&2
      return 1
      ;;
  esac
}

# opencode_v2_store_version reads the newest session_v2.version from the store
# the rig-home row points the daemon at: $XDG_DATA_HOME/opencode/opencode.db.
# It refuses rather than guessing on the two settings that would point it at a
# different store than the 2.x service writes: OPENCODE_DB set (the service runs
# without it, so the daemon would watch another database), and XDG_DATA_HOME
# unset or relative (the default is the operator's real 1.x store).
opencode_v2_store_version() {
  local db ver
  if [[ -n "${OPENCODE_DB:-}" ]]; then
    echo "cli-version: OPENCODE_DB is set ($OPENCODE_DB) — the opencode-v2 rig records the 2.x" >&2
    echo "cli-version:   service's store, and the service runs without it; unset it" >&2
    return 1
  fi
  if [[ "${XDG_DATA_HOME:-}" != /* ]]; then
    echo "cli-version: opencode-v2 needs XDG_DATA_HOME exported as an absolute path (the 2.x" >&2
    echo "cli-version:   service's data dir — see replaydata/agents/opencode-v2/prerequisites.md)" >&2
    return 1
  fi
  db="$XDG_DATA_HOME/opencode/opencode.db"
  if [[ ! -f "$db" ]]; then
    echo "cli-version: no opencode store at $db" >&2
    return 1
  fi
  if ! ver="$(sqlite3 -readonly -cmd '.timeout 2000' "$db" \
      'SELECT version FROM session_v2 ORDER BY time_created DESC, id DESC LIMIT 1' 2>&1)"; then
    echo "cli-version: reading session_v2.version from $db failed: $ver" >&2
    return 1
  fi
  if [[ -z "$ver" ]]; then
    echo "cli-version: $db has no session_v2 row, so it records no 2.x version" >&2
    return 1
  fi
  printf '%s\n' "$ver"
}
