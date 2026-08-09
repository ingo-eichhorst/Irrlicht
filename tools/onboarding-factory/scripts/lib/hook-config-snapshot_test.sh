#!/usr/bin/env bash
# hook-config-snapshot_test.sh — unit tests for hook-config-snapshot.sh. Plain
# bash, no framework. Run directly or via scripts/smoke-test.sh.
#
# The lib is sourced (not run as a subprocess) so the tests drive its two
# functions directly against a fake $HOME / $CODEX_HOME. What matters is that a
# recording daemon's edits to the user's shared agent config never survive the
# run — including the case where the daemon created a config file that was not
# there before (#1178).
#
# The second half of the file is #1357: the protected set is whatever the
# daemon binary declares (`irrlichd --print-hook-configs`), not a literal pair
# of paths this file and the daemon have to agree on by convention; and a file
# is only ever removed on the strength of an explicit "was absent" record, so
# widening that set can never delete a config the snapshot failed to save.

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
pass() { local label="$1"; echo "  PASS: $label"; return 0; }
fail() {
  local label="$1" expected="$2" got="$3"
  echo "  FAIL: $label — expected [$expected] got [$got]"
  fails=$((fails + 1))
  return 0
}
assert_eq() {
  local label="$1" expected="$2" actual="$3"
  [[ "$expected" == "$actual" ]] && pass "$label" || fail "$label" "$expected" "$actual"
  return 0
}

# assert_snapshot_refused <rc> is the tail every "the snapshot must not proceed"
# case shares: it reported failure, and the user's config is untouched.
assert_snapshot_refused() {
  assert_eq "snapshot reports failure" "1" "$1"
  assert_eq "the user's config survives" "user config" \
    "$(cat "$HOME/.claude/settings.json" 2>/dev/null)"
  return 0
}

# fake_hook_config_daemon writes an executable that answers
# --print-hook-configs with the paths given, standing in for the real irrlichd.
# Every case declares its own set, so what the lib protects is visible in the
# case rather than inherited from the adapter set compiled in today. (Named in
# full because spawn-record-daemon_test.sh has a `fake_daemon` that models the
# opposite thing — a process surviving the shutdown ladder — and the two libs
# are sourced together in production.)
fake_hook_config_daemon() {
  local bin="$1"; shift
  printf '%s\n' "$@" > "$bin.paths"
  cat > "$bin" <<EOF
#!/usr/bin/env bash
[[ "\${1:-}" == "--print-hook-configs" ]] || { echo "unexpected args: \$*" >&2; exit 2; }
cat "$bin.paths"
EOF
  chmod +x "$bin"
  return 0
}

# fresh_env points $HOME and $CODEX_HOME at a clean subdir, sets $ROOT, rewrites
# $DAEMON to declare the two config files the shipped adapters install into, and
# clears the lib's state so each case starts from nothing. It must NOT be called
# in a command substitution — the env assignments have to land in this shell,
# not a subshell.
#
# $DAEMON is one shared path rewritten per case rather than a new file each
# time: every case still execs a real subprocess, but macOS charges ~0.3s to
# first-exec a newly created executable, which was most of this file's runtime.
DAEMON="$TMP/irrlichd"
fresh_env() {
  local name="$1"
  ROOT="$TMP/$name"
  mkdir -p "$ROOT/home/.claude" "$ROOT/codex"
  HOME="$ROOT/home"
  CODEX_HOME="$ROOT/codex"
  fake_hook_config_daemon "$DAEMON" "$HOME/.claude/settings.json" "$CODEX_HOME/hooks.json"
  # shellcheck disable=SC2034  # read by the sourced lib's restore_hook_configs
  HOOK_CONFIG_BACKUP_DIR=""
  return 0
}

echo "== an existing config is restored byte-for-byte =="
fresh_env existing
printf '{"hooks":{"Stop":"localhost:7837"}}\n' > "$HOME/.claude/settings.json"
snapshot_hook_configs "$ROOT/backup" "$DAEMON"
printf '{"hooks":{"Stop":"localhost:7838"}}\n' > "$HOME/.claude/settings.json"   # daemon repoints it
restore_hook_configs
assert_eq "settings.json restored" '{"hooks":{"Stop":"localhost:7837"}}' "$(cat "$HOME/.claude/settings.json")"

echo "== a config the daemon created from nothing is removed =="
fresh_env created
snapshot_hook_configs "$ROOT/backup" "$DAEMON"
printf '{"hooks":{}}\n' > "$CODEX_HOME/hooks.json"                               # daemon creates it
restore_hook_configs
[[ -e "$CODEX_HOME/hooks.json" ]] && got=present || got=absent
assert_eq "hooks.json removed" "absent" "$got"

echo "== an untouched config survives a restore unchanged =="
fresh_env untouched
printf 'original\n' > "$CODEX_HOME/hooks.json"
snapshot_hook_configs "$ROOT/backup" "$DAEMON"
restore_hook_configs
assert_eq "hooks.json unchanged" "original" "$(cat "$CODEX_HOME/hooks.json")"

echo "== restore without a snapshot is a harmless no-op that still returns 0 =="
fresh_env nosnapshot
printf 'untouched\n' > "$HOME/.claude/settings.json"
restore_hook_configs
rc=$?
assert_eq "restore returns 0" "0" "$rc"
assert_eq "file left alone" "untouched" "$(cat "$HOME/.claude/settings.json")"

echo "== the protected set is whatever the daemon declares (#1357) =="
# A third hook adapter is a config path this file has never heard of. The rig
# must protect it because the daemon named it, not because someone remembered
# to add a literal here — that is the whole defect: the two lists were correct
# for exactly as long as there were exactly two adapters.
fresh_env registry
mkdir -p "$HOME/.gemini"
printf 'user gemini config\n' > "$HOME/.gemini/settings.json"
fake_hook_config_daemon "$DAEMON" "$HOME/.claude/settings.json" "$CODEX_HOME/hooks.json" "$HOME/.gemini/settings.json"
snapshot_hook_configs "$ROOT/backup" "$DAEMON"
printf 'repointed at a dead recorder port\n' > "$HOME/.gemini/settings.json"      # daemon rewrites it
restore_hook_configs
assert_eq "the third adapter's config is restored too" "user gemini config" \
  "$(cat "$HOME/.gemini/settings.json" 2>/dev/null)"

echo "== a backup that did not land aborts the snapshot instead of arming a delete (#1357) =="
# "No backup for this file" used to mean "the daemon created it, remove it", so
# a snapshot that failed to copy an EXISTING file armed its deletion. Deletion
# must require a positive record that the file was absent — never the absence
# of a record.
fresh_env cpfail
printf 'user config\n' > "$HOME/.claude/settings.json"
mkdir -p "$ROOT/backup/0"        # a DIRECTORY where the backup file must go
snapshot_hook_configs "$ROOT/backup" "$DAEMON"
rc=$?
restore_hook_configs
assert_snapshot_refused "$rc"

echo "== an unwritable backup dir aborts the snapshot too (#1357) =="
fresh_env unwritable
printf 'user config\n' > "$HOME/.claude/settings.json"
mkdir -p "$ROOT/backup"
chmod 500 "$ROOT/backup"
if [[ -w "$ROOT/backup" ]]; then
  echo "  SKIP: running with write access to a 0500 dir (root?)"
else
  snapshot_hook_configs "$ROOT/backup" "$DAEMON"
  rc=$?
  restore_hook_configs
  assert_snapshot_refused "$rc"
fi
chmod 700 "$ROOT/backup" 2>/dev/null || true

echo "== restore acts only on paths the manifest records (#1357) =="
# LOCK, not a regression test: it passes on the pre-#1357 lib too. It pins the
# property the manifest exists to guarantee — restore reads the snapshot's own
# record of what it saved, so nothing else on disk is in scope. The published
# file list that used to sit beside the backups, coupled to them only by array
# index, is gone precisely so there is no second answer to "which files?".
fresh_env unrecorded
printf 'user config\n' > "$HOME/.claude/settings.json"
snapshot_hook_configs "$ROOT/backup" "$DAEMON"
mkdir -p "$HOME/.gemini"
printf 'user gemini config\n' > "$HOME/.gemini/settings.json"   # never declared
restore_hook_configs
assert_eq "the unrecorded file survives" "user gemini config" \
  "$(cat "$HOME/.gemini/settings.json" 2>/dev/null)"

echo "== a daemon that cannot list its config files fails the snapshot loudly (#1357) =="
# Falling back to a literal list here would reinstate the defect at the one
# moment it matters, so there is no fallback: the run stops instead.
fresh_env nolist
printf 'user config\n' > "$HOME/.claude/settings.json"
printf '#!/usr/bin/env bash\nexit 1\n' > "$DAEMON"
chmod +x "$DAEMON"
err="$(snapshot_hook_configs "$ROOT/backup" "$DAEMON" 2>&1)"
rc=$?
restore_hook_configs
assert_snapshot_refused "$rc"
[[ "$err" == *"--print-hook-configs"* ]] && got=yes || got=no
assert_eq "says which command failed" "yes" "$got"

echo "== a directory at a declared config path is left alone, not relocated (#1357) =="
# The snapshot records anything that is not a regular file as "absent". Restore
# must not read that as licence to move a whole directory a user keeps there —
# the rm -f this replaced refused a directory outright, so an asymmetric test
# would have made the safety fix MORE destructive than the bug.
fresh_env notafile
mkdir -p "$CODEX_HOME/hooks.json/precious"
printf 'keep me\n' > "$CODEX_HOME/hooks.json/precious/keep.txt"
snapshot_hook_configs "$ROOT/backup" "$DAEMON"
restore_hook_configs
assert_eq "the directory survives" "keep me" \
  "$(cat "$CODEX_HOME/hooks.json/precious/keep.txt" 2>/dev/null)"

echo "== a daemon that hangs instead of answering is not waited on forever (#1357) =="
# An irrlichd predating the flag does not reject it: hasFlag dispatch ignores
# what it does not recognize and starts a full production daemon, which would
# otherwise hang the capture with a real daemon running and no backup taken.
fresh_env hang
printf 'user config\n' > "$HOME/.claude/settings.json"
printf '#!/usr/bin/env bash\nsleep 120\n' > "$DAEMON"
chmod +x "$DAEMON"
# shellcheck disable=SC2034  # read by the sourced lib's hook_config_paths
HOOK_CONFIG_PROBE_TIMEOUT_S=0.2
snapshot_hook_configs "$ROOT/backup" "$DAEMON" 2>/dev/null
rc=$?
unset HOOK_CONFIG_PROBE_TIMEOUT_S
restore_hook_configs
assert_snapshot_refused "$rc"

echo "== a diagnostic on stderr is never adopted as a config path (#1357) =="
fresh_env noisy
cat > "$DAEMON" <<EOF
#!/usr/bin/env bash
echo "/tmp/not-a-config-warning" >&2
printf '%s\n' "$HOME/.claude/settings.json"
EOF
chmod +x "$DAEMON"
snapshot_hook_configs "$ROOT/backup" "$DAEMON"
assert_eq "only the stdout path is managed" "$HOME/.claude/settings.json" \
  "$(cut -f3 "$ROOT/backup/manifest")"

echo "== a failing re-snapshot does not disarm the one that succeeded (#1357) =="
# HOOK_CONFIG_BACKUP_DIR used to be cleared on the way in, so a second snapshot
# that failed left restore a silent no-op while the EXIT trap that calls it
# stayed armed.
fresh_env resnapshot
printf 'user config\n' > "$HOME/.claude/settings.json"
snapshot_hook_configs "$ROOT/backup" "$DAEMON"
printf 'repointed\n' > "$HOME/.claude/settings.json"
printf '#!/usr/bin/env bash\nexit 1\n' > "$DAEMON"
chmod +x "$DAEMON"
snapshot_hook_configs "$ROOT/backup2" "$DAEMON" 2>/dev/null
rc=$?
restore_hook_configs
assert_eq "the second snapshot is refused" "1" "$rc"
assert_eq "the first snapshot still restores" "user config" \
  "$(cat "$HOME/.claude/settings.json" 2>/dev/null)"

echo "== the lib names no agent config path of its own (#1357) =="
# The literal list is the defect. A path spelled out here is one the daemon
# does not have to agree with, which is how the two drifted in the first place.
LITERAL_RE='\.(claude|codex|gemini)/'
assert_eq "no agent config literals in the lib" "0" \
  "$(grep -cE "$LITERAL_RE" "$DIR/hook-config-snapshot.sh" | tr -d ' ')"

echo ""
if [[ "$fails" -eq 0 ]]; then
  echo "hook-config-snapshot_test: ALL PASS"
  exit 0
fi
echo "hook-config-snapshot_test: $fails FAILURE(S)" >&2
exit 1
