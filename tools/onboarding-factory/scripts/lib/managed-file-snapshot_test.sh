#!/usr/bin/env bash
# managed-file-snapshot_test.sh — unit tests for managed-file-snapshot.sh. Plain
# bash, no framework. Run directly or via scripts/smoke-test.sh.
#
# The lib is sourced (not run as a subprocess) so the tests drive its two
# functions directly against a fake $HOME / $CODEX_HOME. What matters is that a
# recording daemon's edits to the user's shared agent config never survive the
# run — including the case where the daemon created a config file that was not
# there before (#1178).
#
# The second half of the file is #1357: the protected set is whatever the
# daemon binary declares (`irrlichd --print-managed-files`), not a literal pair
# of paths this file and the daemon have to agree on by convention; and a file
# is only ever removed on the strength of an explicit "was absent" record, so
# widening that set can never delete a config the snapshot failed to save.

set -uo pipefail   # NOT -e: assertions capture non-zero return codes

DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=lib/managed-file-snapshot.sh
source "$DIR/managed-file-snapshot.sh"

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

# fake_managed_file_daemon writes an executable that answers
# --print-managed-files with the paths given, standing in for the real irrlichd.
# Every case declares its own set, so what the lib protects is visible in the
# case rather than inherited from the adapter set compiled in today. (Named in
# full because spawn-record-daemon_test.sh has a `fake_daemon` that models the
# opposite thing — a process surviving the shutdown ladder — and the two libs
# are sourced together in production.)
fake_managed_file_daemon() {
  local bin="$1"; shift
  printf '%s\n' "$@" > "$bin.paths"
  cat > "$bin" <<EOF
#!/usr/bin/env bash
[[ "\${1:-}" == "--print-managed-files" ]] || { echo "unexpected args: \$*" >&2; exit 2; }
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
  fake_managed_file_daemon "$DAEMON" "$HOME/.claude/settings.json" "$CODEX_HOME/hooks.json"
  # shellcheck disable=SC2034  # read by the sourced lib's restore_managed_files
  MANAGED_FILE_BACKUP_DIR=""
  MANAGED_FILE_SEAL_DIR=""
  MANAGED_FILE_STRICT_SEAL=0
  MANAGED_FILE_ORACLE_REQUIRED=0
  MANAGED_FILE_ORACLE_DIR=""
  return 0
}

echo "== an existing config is restored byte-for-byte =="
fresh_env existing
printf '{"hooks":{"Stop":"localhost:7837"}}\n' > "$HOME/.claude/settings.json"
snapshot_managed_files "$ROOT/backup" "$DAEMON"
printf '{"hooks":{"Stop":"localhost:7838"}}\n' > "$HOME/.claude/settings.json"   # daemon repoints it
restore_managed_files
assert_eq "settings.json restored" '{"hooks":{"Stop":"localhost:7837"}}' "$(cat "$HOME/.claude/settings.json")"

echo "== a strict seal restores the expected daemon hook edit =="
fresh_env sealed
printf '{"hooks":{"Stop":"localhost:7837"}}\n' > "$HOME/.claude/settings.json"
snapshot_managed_files "$ROOT/backup" "$DAEMON"
printf '{"hooks":{"Stop":"localhost:7838"}}\n' > "$HOME/.claude/settings.json"
seal_managed_files
restore_managed_files
assert_eq "sealed daemon edit is restored" '{"hooks":{"Stop":"localhost:7837"}}' \
  "$(cat "$HOME/.claude/settings.json")"

echo "== an oracle rejects an unrelated edit made before sealing =="
# MUTATION fixture: the daemon's expected output does not contain the unrelated
# line. A seal that only snapshots current bytes accepts this edit and later
# destroys it. Oracle-backed sealing must refuse it and keep recovery active.
fresh_env oracle_mismatch
MANAGED_FILE_STRICT_SEAL=1
# shellcheck disable=SC2034  # read by the sourced seal implementation
MANAGED_FILE_ORACLE_REQUIRED=1
printf 'user baseline\n' > "$HOME/.claude/settings.json"
snapshot_managed_files "$ROOT/backup" "$DAEMON"
mkdir "$ROOT/oracle"
printf 'expected daemon hook\n' > "$ROOT/oracle/0"
printf 'file\t0\t%s\nabsent\t1\t%s\n' \
  "$HOME/.claude/settings.json" "$CODEX_HOME/hooks.json" > "$ROOT/oracle/manifest"
# shellcheck disable=SC2034  # read by the sourced seal implementation
MANAGED_FILE_ORACLE_DIR="$ROOT/oracle"
printf 'expected daemon hook\nunrelated edit\n' > "$HOME/.claude/settings.json"
seal_managed_files 2>/dev/null
rc=$?
assert_eq "pre-seal unrelated edit refuses seal" "1" "$rc"
[[ -n "$MANAGED_FILE_BACKUP_DIR" ]] && got=active || got=cleared
assert_eq "oracle refusal keeps recovery snapshot" "active" "$got"
MANAGED_FILE_BACKUP_DIR=""
MANAGED_FILE_SEAL_DIR=""
MANAGED_FILE_STRICT_SEAL=0

echo "== pre-seal cleanup accepts only one complete Apply-state variant =="
# MUTATION fixture: this models a middle permission whose service guard failed
# while a later permission still applied. No final seal exists. After daemon
# shutdown, cleanup may restore only because the complete tree matches one
# state produced by that ordered subset of the real Apply closures.
fresh_env preseal_variant
MANAGED_FILE_STRICT_SEAL=1
MANAGED_FILE_ORACLE_REQUIRED=1
printf 'settings baseline\n' > "$HOME/.claude/settings.json"
snapshot_managed_files "$ROOT/backup" "$DAEMON"
mkdir -p "$ROOT/oracle/states/5"
printf 'settings first effect\n' > "$ROOT/oracle/states/5/0"
printf 'later effect\n' > "$ROOT/oracle/states/5/1"
printf 'file\t0\t%s\nfile\t1\t%s\n' \
  "$HOME/.claude/settings.json" "$CODEX_HOME/hooks.json" > "$ROOT/oracle/states/5/manifest"
MANAGED_FILE_ORACLE_DIR="$ROOT/oracle"
printf 'settings first effect\n' > "$HOME/.claude/settings.json"
printf 'later effect\n' > "$CODEX_HOME/hooks.json"
restore_managed_files 2>/dev/null
rc=$?
assert_eq "matching pre-seal variant restores" "0" "$rc"
assert_eq "matching variant restores baseline bytes" "settings baseline" "$(cat "$HOME/.claude/settings.json")"
[[ -e "$CODEX_HOME/hooks.json" ]] && got=present || got=absent
assert_eq "matching variant restores baseline absence" "absent" "$got"
[[ -z "$MANAGED_FILE_BACKUP_DIR" && -z "$MANAGED_FILE_SEAL_DIR" ]] && got=cleared || got=active
assert_eq "successful recovered restore clears state" "cleared" "$got"

fresh_env preseal_no_match
MANAGED_FILE_STRICT_SEAL=1
MANAGED_FILE_ORACLE_REQUIRED=1
printf 'settings baseline\n' > "$HOME/.claude/settings.json"
snapshot_managed_files "$ROOT/backup" "$DAEMON"
mkdir -p "$ROOT/oracle/states/5"
printf 'expected effect\n' > "$ROOT/oracle/states/5/0"
printf 'absent\t1\t%s\nfile\t0\t%s\n' \
  "$CODEX_HOME/hooks.json" "$HOME/.claude/settings.json" > "$ROOT/oracle/states/5/manifest"
MANAGED_FILE_ORACLE_DIR="$ROOT/oracle"
printf 'unrelated external bytes\n' > "$HOME/.claude/settings.json"
restore_managed_files 2>/dev/null
rc=$?
assert_eq "unmatched pre-seal tree refuses restore" "1" "$rc"
assert_eq "unmatched bytes survive" "unrelated external bytes" "$(cat "$HOME/.claude/settings.json")"
[[ -n "$MANAGED_FILE_BACKUP_DIR" ]] && got=active || got=cleared
assert_eq "unmatched pre-seal tree keeps recovery state" "active" "$got"
MANAGED_FILE_BACKUP_DIR=""
MANAGED_FILE_SEAL_DIR=""
MANAGED_FILE_STRICT_SEAL=0
# shellcheck disable=SC2034  # not read by this file: these reset the SOURCED
#                               managed-file-snapshot.sh oracle state after the fixture.
MANAGED_FILE_ORACLE_REQUIRED=0
# shellcheck disable=SC2034  # see above — a directive covers only the next line
MANAGED_FILE_ORACLE_DIR=""

echo "== a managed path cannot be a baseline symlink =="
# MUTATION fixture: the declared config path resolves to a different file.
# Snapshot must reject it instead of arming a later restore through the link.
fresh_env baseline_symlink
printf 'external baseline\n' > "$ROOT/external"
ln -s "$ROOT/external" "$HOME/.claude/settings.json"
snapshot_managed_files "$ROOT/backup" "$DAEMON" 2>/dev/null
rc=$?
assert_eq "baseline symlink refuses snapshot" "1" "$rc"
assert_eq "baseline symlink target survives" "external baseline" "$(cat "$ROOT/external")"

fresh_env baseline_dangling_symlink
ln -s "$ROOT/missing-target" "$HOME/.claude/settings.json"
snapshot_managed_files "$ROOT/backup" "$DAEMON" 2>/dev/null
rc=$?
assert_eq "dangling baseline symlink refuses snapshot" "1" "$rc"
[[ -L "$HOME/.claude/settings.json" ]] && got="link" || got="changed"
assert_eq "dangling baseline symlink survives" "link" "$got"

echo "== a managed path cannot become a symlink while sealing =="
# MUTATION fixture: a replacement link points at bytes that look exactly like
# the daemon edit. Seal must use path type, not the followed target contents.
fresh_env seal_symlink
MANAGED_FILE_STRICT_SEAL=1
printf 'user baseline\n' > "$HOME/.claude/settings.json"
snapshot_managed_files "$ROOT/backup" "$DAEMON"
printf 'expected daemon hook\n' > "$ROOT/external"
rm "$HOME/.claude/settings.json"
ln -s "$ROOT/external" "$HOME/.claude/settings.json"
seal_managed_files 2>/dev/null
rc=$?
assert_eq "seal-time symlink refuses seal" "1" "$rc"
restore_managed_files 2>/dev/null
restore_rc=$?
assert_eq "failed symlink seal blocks restore" "1" "$restore_rc"
assert_eq "seal-time symlink target survives" "expected daemon hook" "$(cat "$ROOT/external")"
MANAGED_FILE_BACKUP_DIR=""
MANAGED_FILE_SEAL_DIR=""
MANAGED_FILE_STRICT_SEAL=0

fresh_env seal_dangling_symlink
MANAGED_FILE_STRICT_SEAL=1
printf 'user baseline\n' > "$HOME/.claude/settings.json"
snapshot_managed_files "$ROOT/backup" "$DAEMON"
rm "$HOME/.claude/settings.json"
ln -s "$ROOT/missing-target" "$HOME/.claude/settings.json"
seal_managed_files 2>/dev/null
rc=$?
assert_eq "dangling seal-time symlink refuses seal" "1" "$rc"
MANAGED_FILE_BACKUP_DIR=""
MANAGED_FILE_SEAL_DIR=""
MANAGED_FILE_STRICT_SEAL=0

echo "== a managed path cannot become a symlink after sealing =="
# MUTATION fixture: matching target bytes would make cmp pass if it followed
# the replacement link. Strict restore must refuse before any path is written.
fresh_env restore_symlink
MANAGED_FILE_STRICT_SEAL=1
printf 'user baseline\n' > "$HOME/.claude/settings.json"
snapshot_managed_files "$ROOT/backup" "$DAEMON"
printf 'expected daemon hook\n' > "$HOME/.claude/settings.json"
seal_managed_files
printf 'expected daemon hook\n' > "$ROOT/external"
rm "$HOME/.claude/settings.json"
ln -s "$ROOT/external" "$HOME/.claude/settings.json"
restore_managed_files 2>/dev/null
rc=$?
assert_eq "post-seal symlink refuses restore" "1" "$rc"
assert_eq "post-seal symlink target survives" "expected daemon hook" "$(cat "$ROOT/external")"
MANAGED_FILE_BACKUP_DIR=""
MANAGED_FILE_SEAL_DIR=""
MANAGED_FILE_STRICT_SEAL=0

echo "== a managed path cannot traverse a retargeted parent symlink =="
fresh_env parent_symlink
MANAGED_FILE_STRICT_SEAL=1
tree_a="$ROOT/tree-a"
tree_b="$ROOT/tree-b"
mkdir -p "$tree_a" "$tree_b"
rm -rf "$HOME/.claude"
ln -s "$tree_a" "$HOME/.claude"
printf 'user baseline\n' > "$tree_a/settings.json"
printf 'expected daemon hook\n' > "$tree_b/settings.json"
snapshot_managed_files "$ROOT/backup" "$DAEMON" 2>/dev/null
snapshot_rc=$?
if [[ "$snapshot_rc" -eq 0 ]]; then
  printf 'expected daemon hook\n' > "$tree_a/settings.json"
  seal_managed_files 2>/dev/null
  seal_rc=$?
  rm "$HOME/.claude"
  ln -s "$tree_b" "$HOME/.claude"
  restore_managed_files 2>/dev/null
  restore_rc=$?
else
  seal_rc=1
  restore_rc=1
fi
assert_eq "parent symlink is refused before snapshot" "1" "$snapshot_rc"
assert_eq "parent symlink cannot be sealed" "1" "$seal_rc"
assert_eq "retargeted parent symlink refuses restore" "1" "$restore_rc"
assert_eq "retargeted tree is not overwritten" "expected daemon hook" "$(cat "$tree_b/settings.json")"
MANAGED_FILE_BACKUP_DIR=""
MANAGED_FILE_SEAL_DIR=""
MANAGED_FILE_STRICT_SEAL=0

echo "== restore rejects a replaced or escaping snapshot manifest =="
fresh_env manifest_symlink
printf 'user baseline\n' > "$HOME/.claude/settings.json"
snapshot_managed_files "$ROOT/backup" "$DAEMON"
printf 'daemon bytes\n' > "$HOME/.claude/settings.json"
mv "$ROOT/backup/manifest" "$ROOT/manifest-real"
ln -s "$ROOT/manifest-real" "$ROOT/backup/manifest"
restore_managed_files 2>/dev/null
rc=$?
assert_eq "snapshot manifest symlink refuses restore" "1" "$rc"
assert_eq "manifest-link refusal preserves current bytes" "daemon bytes" "$(cat "$HOME/.claude/settings.json")"
MANAGED_FILE_BACKUP_DIR=""

fresh_env manifest_escape
printf 'user baseline\n' > "$HOME/.claude/settings.json"
snapshot_managed_files "$ROOT/backup" "$DAEMON"
printf 'daemon bytes\n' > "$HOME/.claude/settings.json"
sed 's/^saved\t0\t/saved\t..\/escaped\t/' "$ROOT/backup/manifest" > "$ROOT/manifest-mutated"
mv "$ROOT/manifest-mutated" "$ROOT/backup/manifest"
restore_managed_files 2>/dev/null
rc=$?
assert_eq "escaping snapshot slot refuses restore" "1" "$rc"
assert_eq "escaping-slot refusal preserves current bytes" "daemon bytes" "$(cat "$HOME/.claude/settings.json")"
MANAGED_FILE_BACKUP_DIR=""

fresh_env restore_dangling_symlink
MANAGED_FILE_STRICT_SEAL=1
snapshot_managed_files "$ROOT/backup" "$DAEMON"
seal_managed_files
ln -s "$ROOT/missing-target" "$HOME/.claude/settings.json"
restore_managed_files 2>/dev/null
rc=$?
assert_eq "dangling post-seal symlink refuses restore" "1" "$rc"
[[ -L "$HOME/.claude/settings.json" ]] && got="link" || got="changed"
assert_eq "dangling post-seal symlink survives" "link" "$got"
MANAGED_FILE_BACKUP_DIR=""
MANAGED_FILE_SEAL_DIR=""
MANAGED_FILE_STRICT_SEAL=0

echo "== a strict seal refuses to overwrite a concurrent external edit =="
# MUTATION fixture: the first edit is the expected daemon hook install. The
# second edit simulates another process changing the same config after the
# driver sealed that expected state. Restore must fail before copying anything.
fresh_env concurrent_edit
printf 'user baseline\n' > "$HOME/.claude/settings.json"
snapshot_managed_files "$ROOT/backup" "$DAEMON"
printf 'expected daemon hook\n' > "$HOME/.claude/settings.json"
seal_managed_files
printf 'concurrent external edit\n' > "$HOME/.claude/settings.json"
err="$(restore_managed_files 2>&1)"
rc=$?
assert_eq "concurrent edit refuses restore" "1" "$rc"
assert_eq "concurrent bytes are not overwritten" "concurrent external edit" \
  "$(cat "$HOME/.claude/settings.json")"
[[ "$err" == *"refusing to overwrite concurrent change"* ]] && got=yes || got=no
assert_eq "refusal names the conflict" "yes" "$got"
# Clear the still-active snapshot handle. The refusal deliberately keeps it for
# manual recovery, but this test owns the disposable tree.
# shellcheck disable=SC2034  # shared state read by the sourced snapshot library
MANAGED_FILE_BACKUP_DIR=""
# shellcheck disable=SC2034  # shared state read by the sourced snapshot library
MANAGED_FILE_SEAL_DIR=""

echo "== each sealed path is rechecked immediately before restore =="
# MUTATION fixture: preserve_replaced runs after the all-files precheck. Its
# temporary override changes the later path at that exact boundary. The later
# restore must compare that path with its seal again and keep recovery active.
fresh_env per_entry_recheck
MANAGED_FILE_STRICT_SEAL=1
printf 'settings baseline\n' > "$HOME/.claude/settings.json"
printf 'hooks baseline\n' > "$CODEX_HOME/hooks.json"
snapshot_managed_files "$ROOT/backup" "$DAEMON"
printf 'settings daemon\n' > "$HOME/.claude/settings.json"
printf 'hooks daemon\n' > "$CODEX_HOME/hooks.json"
seal_managed_files
original_preserve="$(declare -f preserve_replaced)"
preserve_replaced() {
  if [[ "$2" == "$HOME/.claude/settings.json" ]]; then
    printf 'hooks concurrent edit\n' > "$CODEX_HOME/hooks.json"
  fi
  return 0
}
restore_managed_files 2>/dev/null
rc=$?
eval "$original_preserve"
assert_eq "per-entry changed bytes refuse restore" "1" "$rc"
assert_eq "later concurrent bytes survive" "hooks concurrent edit" "$(cat "$CODEX_HOME/hooks.json")"
[[ -n "$MANAGED_FILE_BACKUP_DIR" && -n "$MANAGED_FILE_SEAL_DIR" ]] && got=active || got=cleared
assert_eq "per-entry refusal keeps recovery state" "active" "$got"
MANAGED_FILE_BACKUP_DIR=""
MANAGED_FILE_SEAL_DIR=""
MANAGED_FILE_STRICT_SEAL=0

fresh_env per_entry_delete
MANAGED_FILE_STRICT_SEAL=1
printf 'settings baseline\n' > "$HOME/.claude/settings.json"
printf 'hooks baseline\n' > "$CODEX_HOME/hooks.json"
snapshot_managed_files "$ROOT/backup" "$DAEMON"
printf 'settings daemon\n' > "$HOME/.claude/settings.json"
printf 'hooks daemon\n' > "$CODEX_HOME/hooks.json"
seal_managed_files
original_preserve="$(declare -f preserve_replaced)"
preserve_replaced() {
  if [[ "$2" == "$HOME/.claude/settings.json" ]]; then
    rm "$CODEX_HOME/hooks.json"
  fi
  return 0
}
restore_managed_files 2>/dev/null
rc=$?
eval "$original_preserve"
assert_eq "per-entry deletion refuses restore" "1" "$rc"
[[ -e "$CODEX_HOME/hooks.json" ]] && got=restored || got=absent
assert_eq "deleted later path is not recreated" "absent" "$got"
[[ -n "$MANAGED_FILE_BACKUP_DIR" && -n "$MANAGED_FILE_SEAL_DIR" ]] && got=active || got=cleared
assert_eq "per-entry deletion keeps recovery state" "active" "$got"
MANAGED_FILE_BACKUP_DIR=""
MANAGED_FILE_SEAL_DIR=""
MANAGED_FILE_STRICT_SEAL=0

fresh_env per_entry_absent_creation
MANAGED_FILE_STRICT_SEAL=1
printf 'settings baseline\n' > "$HOME/.claude/settings.json"
snapshot_managed_files "$ROOT/backup" "$DAEMON"
printf 'settings daemon\n' > "$HOME/.claude/settings.json"
seal_managed_files
original_preserve="$(declare -f preserve_replaced)"
preserve_replaced() {
  if [[ "$2" == "$HOME/.claude/settings.json" ]]; then
    printf 'concurrent creation\n' > "$CODEX_HOME/hooks.json"
  fi
  return 0
}
restore_managed_files 2>/dev/null
rc=$?
eval "$original_preserve"
assert_eq "per-entry expected-absent creation refuses restore" "1" "$rc"
assert_eq "concurrent expected-absent bytes survive" "concurrent creation" "$(cat "$CODEX_HOME/hooks.json")"
[[ -n "$MANAGED_FILE_BACKUP_DIR" && -n "$MANAGED_FILE_SEAL_DIR" ]] && got=active || got=cleared
assert_eq "expected-absent refusal keeps recovery state" "active" "$got"
MANAGED_FILE_BACKUP_DIR=""
MANAGED_FILE_SEAL_DIR=""
MANAGED_FILE_STRICT_SEAL=0

echo "== a failed strict seal cannot fall back to an unchecked restore =="
# MUTATION fixture: the second managed path becomes a directory after the
# daemon edits the first path. This forces a failure after sealing started.
# Cleanup must leave the first path unchanged and retain the recovery snapshot.
fresh_env failed_seal
MANAGED_FILE_STRICT_SEAL=1
printf 'user baseline\n' > "$HOME/.claude/settings.json"
snapshot_managed_files "$ROOT/backup" "$DAEMON"
printf 'expected daemon hook\n' > "$HOME/.claude/settings.json"
mkdir "$CODEX_HOME/hooks.json"
if seal_managed_files 2>/dev/null; then
  fail "mid-seal failure is observed" "non-zero" "seal unexpectedly passed"
else
  pass "mid-seal failure is observed"
fi
if restore_managed_files 2>/dev/null; then
  fail "failed strict seal blocks restore" "non-zero" "restore fell back to unchecked bytes"
else
  pass "failed strict seal blocks restore"
fi
assert_eq "failed seal leaves current bytes" "expected daemon hook" \
  "$(cat "$HOME/.claude/settings.json")"
[[ -n "$MANAGED_FILE_BACKUP_DIR" ]] && got=active || got=cleared
assert_eq "failed seal keeps recovery snapshot active" "active" "$got"
MANAGED_FILE_BACKUP_DIR=""
# shellcheck disable=SC2034  # shared state read by the sourced snapshot library
MANAGED_FILE_SEAL_DIR=""
# shellcheck disable=SC2034  # shared state read by the sourced snapshot library
MANAGED_FILE_STRICT_SEAL=0

echo "== a config the daemon created from nothing is removed =="
fresh_env created
snapshot_managed_files "$ROOT/backup" "$DAEMON"
printf '{"hooks":{}}\n' > "$CODEX_HOME/hooks.json"                               # daemon creates it
restore_managed_files
[[ -e "$CODEX_HOME/hooks.json" ]] && got=present || got=absent
assert_eq "hooks.json removed" "absent" "$got"

echo "== an untouched config survives a restore unchanged =="
fresh_env untouched
printf 'original\n' > "$CODEX_HOME/hooks.json"
snapshot_managed_files "$ROOT/backup" "$DAEMON"
restore_managed_files
assert_eq "hooks.json unchanged" "original" "$(cat "$CODEX_HOME/hooks.json")"

echo "== restore without a snapshot is a harmless no-op that still returns 0 =="
fresh_env nosnapshot
printf 'untouched\n' > "$HOME/.claude/settings.json"
restore_managed_files
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
fake_managed_file_daemon "$DAEMON" "$HOME/.claude/settings.json" "$CODEX_HOME/hooks.json" "$HOME/.gemini/settings.json"
snapshot_managed_files "$ROOT/backup" "$DAEMON"
printf 'repointed at a dead recorder port\n' > "$HOME/.gemini/settings.json"      # daemon rewrites it
restore_managed_files
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
snapshot_managed_files "$ROOT/backup" "$DAEMON"
rc=$?
restore_managed_files
assert_snapshot_refused "$rc"

echo "== an unwritable backup dir aborts the snapshot too (#1357) =="
fresh_env unwritable
printf 'user config\n' > "$HOME/.claude/settings.json"
mkdir -p "$ROOT/backup"
chmod 500 "$ROOT/backup"
if [[ -w "$ROOT/backup" ]]; then
  echo "  SKIP: running with write access to a 0500 dir (root?)"
else
  snapshot_managed_files "$ROOT/backup" "$DAEMON"
  rc=$?
  restore_managed_files
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
snapshot_managed_files "$ROOT/backup" "$DAEMON"
mkdir -p "$HOME/.gemini"
printf 'user gemini config\n' > "$HOME/.gemini/settings.json"   # never declared
restore_managed_files
assert_eq "the unrecorded file survives" "user gemini config" \
  "$(cat "$HOME/.gemini/settings.json" 2>/dev/null)"

echo "== a daemon that cannot list its config files fails the snapshot loudly (#1357) =="
# Falling back to a literal list here would reinstate the defect at the one
# moment it matters, so there is no fallback: the run stops instead.
fresh_env nolist
printf 'user config\n' > "$HOME/.claude/settings.json"
printf '#!/usr/bin/env bash\nexit 1\n' > "$DAEMON"
chmod +x "$DAEMON"
err="$(snapshot_managed_files "$ROOT/backup" "$DAEMON" 2>&1)"
rc=$?
restore_managed_files
assert_snapshot_refused "$rc"
[[ "$err" == *"--print-managed-files"* ]] && got=yes || got=no
assert_eq "says which command failed" "yes" "$got"

echo "== a directory at a declared config path is left alone, not relocated (#1357) =="
# The snapshot records anything that is not a regular file as "absent". Restore
# must not read that as licence to move a whole directory a user keeps there —
# the rm -f this replaced refused a directory outright, so an asymmetric test
# would have made the safety fix MORE destructive than the bug.
fresh_env notafile
mkdir -p "$CODEX_HOME/hooks.json/precious"
printf 'keep me\n' > "$CODEX_HOME/hooks.json/precious/keep.txt"
snapshot_managed_files "$ROOT/backup" "$DAEMON"
restore_managed_files
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
# shellcheck disable=SC2034  # read by the sourced lib's managed_file_paths
MANAGED_FILE_PROBE_TIMEOUT_S=0.2
snapshot_managed_files "$ROOT/backup" "$DAEMON" 2>/dev/null
rc=$?
unset MANAGED_FILE_PROBE_TIMEOUT_S
restore_managed_files
assert_snapshot_refused "$rc"

echo "== a diagnostic on stderr is never adopted as a config path (#1357) =="
fresh_env noisy
cat > "$DAEMON" <<EOF
#!/usr/bin/env bash
echo "/tmp/not-a-config-warning" >&2
printf '%s\n' "$HOME/.claude/settings.json"
EOF
chmod +x "$DAEMON"
snapshot_managed_files "$ROOT/backup" "$DAEMON"
assert_eq "only the stdout path is managed" "$HOME/.claude/settings.json" \
  "$(cut -f3 "$ROOT/backup/manifest")"

echo "== a failing re-snapshot does not disarm the one that succeeded (#1357) =="
# MANAGED_FILE_BACKUP_DIR used to be cleared on the way in, so a second snapshot
# that failed left restore a silent no-op while the EXIT trap that calls it
# stayed armed.
fresh_env resnapshot
printf 'user config\n' > "$HOME/.claude/settings.json"
snapshot_managed_files "$ROOT/backup" "$DAEMON"
printf 'repointed\n' > "$HOME/.claude/settings.json"
printf '#!/usr/bin/env bash\nexit 1\n' > "$DAEMON"
chmod +x "$DAEMON"
snapshot_managed_files "$ROOT/backup2" "$DAEMON" 2>/dev/null
rc=$?
restore_managed_files
assert_eq "the second snapshot is refused" "1" "$rc"
assert_eq "the first snapshot still restores" "user config" \
  "$(cat "$HOME/.claude/settings.json" 2>/dev/null)"

echo "== a user's own instruction file is restored byte-for-byte (#1383) =="
# LOCK, not a regression test: this lib is path-agnostic, so it handles a
# CLAUDE.md exactly as it handles a settings.json and this case passes on
# origin/main too. The defect #1383 fixes is upstream of here — the DAEMON never
# named these files, so they were never in the set this lib was handed. Pinning
# them anyway is what makes the widened set's behaviour explicit: an instruction
# file is prose the user wrote, not a config irrlicht generated, so "restored"
# has to mean byte-for-byte including the trailing newline and the blank line
# the managed block was separated by.
fresh_env memory
fake_managed_file_daemon "$DAEMON" \
  "$HOME/.claude/settings.json" "$HOME/.claude/CLAUDE.md" "$HOME/.config/kitty/kitty.conf"
USER_MEMORY=$'# My rules\n\nAlways run the tests.'
printf '%s\n' "$USER_MEMORY" > "$HOME/.claude/CLAUDE.md"
BEFORE="$(cksum < "$HOME/.claude/CLAUDE.md")"
snapshot_managed_files "$ROOT/backup" "$DAEMON"
# grant-all runs the instructions permission's Apply: a managed block is
# appended after a blank separator line.
printf '\n%s\n%s\n' '<!-- BEGIN IRRLICHT MANAGED BLOCK (task-eta) -->' \
  '<!-- END IRRLICHT MANAGED BLOCK (task-eta) -->' >> "$HOME/.claude/CLAUDE.md"
restore_managed_files
assert_eq "the user's instruction file is byte-identical" "$BEFORE" \
  "$(cksum < "$HOME/.claude/CLAUDE.md")"
assert_eq "no managed block survives" "0" \
  "$(grep -c 'IRRLICHT MANAGED BLOCK' "$HOME/.claude/CLAUDE.md" | tr -d ' ')"

echo "== a host config the daemon created is moved aside, not destroyed (#1383) =="
# The kitty remote-control patch creates its file (and its parent directory)
# when absent, so a recording on a machine with no kitty.conf leaves one behind.
# "absent" must mean the path ends up absent AND the content stays recoverable:
# it is a shared config the user may start writing the moment the recording is
# over, and the run has no way to tell irrlicht's block from theirs by then.
fresh_env hostconf
fake_managed_file_daemon "$DAEMON" \
  "$HOME/.claude/settings.json" "$HOME/.config/kitty/kitty.conf"
snapshot_managed_files "$ROOT/backup" "$DAEMON"
mkdir -p "$HOME/.config/kitty"                                        # the Apply closure creates both
printf 'allow_remote_control yes\nlisten_on unix:/tmp/kitty\n' > "$HOME/.config/kitty/kitty.conf"
restore_managed_files
[[ -e "$HOME/.config/kitty/kitty.conf" ]] && got=present || got=absent
assert_eq "the created host config is gone from its real path" "absent" "$got"
assert_eq "its contents are still recoverable" "allow_remote_control yes" \
  "$(head -1 "$ROOT/backup/created/1" 2>/dev/null)"

echo "== a multi-purpose file the daemon created mid-run keeps the user's own content (#1383) =="
# The sharpest case for move-aside over rm -f, and the reason PR #1375 built it:
# the user had no CLAUDE.md, the recording daemon created one for its managed
# block, and then the AGENT being recorded wrote the user's own instructions
# into the same file during the very run. Deleting it would destroy content that
# was never irrlicht's; restoring the pre-run state still has to mean the path
# is absent.
fresh_env multipurpose
fake_managed_file_daemon "$DAEMON" "$HOME/.claude/settings.json" "$HOME/.claude/CLAUDE.md"
snapshot_managed_files "$ROOT/backup" "$DAEMON"
printf '%s\n' '<!-- BEGIN IRRLICHT MANAGED BLOCK (task-eta) -->' \
  'a rule the user added during the run' > "$HOME/.claude/CLAUDE.md"
restore_managed_files
[[ -e "$HOME/.claude/CLAUDE.md" ]] && got=present || got=absent
assert_eq "the pre-run state (absent) is restored" "absent" "$got"
assert_eq "the user's mid-run content is recoverable" "a rule the user added during the run" \
  "$(tail -1 "$ROOT/backup/created/1" 2>/dev/null)"

echo "== a directory the run created for an absent file is removed too (#1383) =="
# The residue that survived moving the file aside. grant-all runs the kitty
# Apply on a machine with no kitty, whose write goes through
# core/pkg/atomicfile (WriteFile), which mkdir -p's ~/.config/kitty on the
# way to the file; KittyDetected() returns true on the
# mere EXISTENCE of that directory, so leaving it behind makes the user's
# production daemon offer the kitty permission in the wizard from then on.
fresh_env createddir
fake_managed_file_daemon "$DAEMON" \
  "$HOME/.claude/settings.json" "$HOME/.config/kitty/kitty.conf"
snapshot_managed_files "$ROOT/backup" "$DAEMON"
mkdir -p "$HOME/.config/kitty"                                # the Apply closure creates both levels
printf 'allow_remote_control yes\n' > "$HOME/.config/kitty/kitty.conf"
restore_managed_files
[[ -d "$HOME/.config/kitty" ]] && got=present || got=absent
assert_eq "the created config dir is gone" "absent" "$got"
[[ -d "$HOME/.config" ]] && got=present || got=absent
assert_eq "its created parent is gone too" "absent" "$got"

echo "== a created directory the user has since filled is left alone (#1383) =="
# rmdir refuses a non-empty directory, so "the run made this dir" never becomes
# licence to delete what someone else put in it. This is the safety half of the
# case above and the reason no -e/-f inference is needed there.
fresh_env createddir_inuse
fake_managed_file_daemon "$DAEMON" "$HOME/.config/kitty/kitty.conf"
snapshot_managed_files "$ROOT/backup" "$DAEMON"
mkdir -p "$HOME/.config/kitty"
printf 'allow_remote_control yes\n' > "$HOME/.config/kitty/kitty.conf"
printf 'my own theme\n' > "$HOME/.config/kitty/theme.conf"    # the user's file, same dir
restore_managed_files
assert_eq "the user's own file in that dir survives" "my own theme" \
  "$(cat "$HOME/.config/kitty/theme.conf" 2>/dev/null)"

echo "== a pre-existing file changed during the run stays recoverable (#1383) =="
# The asymmetry the widening exposed: 'absent' refuses to destroy content the
# AGENT wrote mid-run, but 'saved' used to cp straight over it. ~/.claude/CLAUDE.md
# is prose written by the very agent the rig drives, so the run-time version has
# to survive somewhere even though the pre-run bytes are what goes back.
fresh_env replaced
fake_managed_file_daemon "$DAEMON" "$HOME/.claude/CLAUDE.md"
printf 'the user rules\n' > "$HOME/.claude/CLAUDE.md"
snapshot_managed_files "$ROOT/backup" "$DAEMON"
printf 'the user rules\na rule the agent added during the run\n' > "$HOME/.claude/CLAUDE.md"
restore_managed_files
assert_eq "the pre-run bytes are what goes back" "the user rules" \
  "$(cat "$HOME/.claude/CLAUDE.md" 2>/dev/null)"
assert_eq "the run-time version is recoverable" "a rule the agent added during the run" \
  "$(tail -1 "$ROOT/backup/replaced/0" 2>/dev/null)"

echo "== an unchanged file is not copied aside on restore (#1383) =="
# LOCK: the common case is a config the daemon rewrote with content we discard
# on purpose. Keeping a copy of every restored file would bury the one that
# matters under a stderr line per file.
fresh_env unchanged_norestore
fake_managed_file_daemon "$DAEMON" "$HOME/.claude/CLAUDE.md"
printf 'the user rules\n' > "$HOME/.claude/CLAUDE.md"
snapshot_managed_files "$ROOT/backup" "$DAEMON"
restore_managed_files
[[ -e "$ROOT/backup/replaced" ]] && got=present || got=absent
assert_eq "nothing was copied aside" "absent" "$got"

echo "== warn_advisory_files reports without touching anything (#1748) =="
# fake_advisory_daemon answers --print-advisory-files with the tab-separated
# "<path>\t<reason>" lines given, standing in for the real irrlichd.
fake_advisory_daemon() {
  local bin="$1"; shift
  printf '%s\n' "$@" > "$bin.advisory"
  cat > "$bin" <<EOF
#!/usr/bin/env bash
[[ "\${1:-}" == "--print-advisory-files" ]] || { echo "unexpected args: \$*" >&2; exit 2; }
cat "$bin.advisory"
EOF
  chmod +x "$bin"
  return 0
}

fresh_env advisory_basic
fake_advisory_daemon "$DAEMON" "$(printf '/tmp/kiro/data.sqlite3\tkiro-cli own store')"
out="$(warn_advisory_files "$DAEMON" 2>&1)"
rc=$?
assert_eq "warn_advisory_files always succeeds" "0" "$rc"
assert_eq "the warning names the path" "1" \
  "$(grep -c "/tmp/kiro/data.sqlite3" <<<"$out" | tr -d ' ')"
assert_eq "the warning is marked ADVISORY" "1" \
  "$(grep -c "ADVISORY" <<<"$out" | tr -d ' ')"
assert_eq "the warning names the reason" "1" \
  "$(grep -c "kiro-cli own store" <<<"$out" | tr -d ' ')"
assert_eq "nothing is backed up" "absent" \
  "$([[ -e "$ROOT/managed-file-backup" ]] && echo present || echo absent)"

echo "== warn_advisory_files is silent and succeeds with nothing declared =="
fresh_env advisory_empty
fake_advisory_daemon "$DAEMON"
out="$(warn_advisory_files "$DAEMON" 2>&1)"
rc=$?
assert_eq "warn_advisory_files succeeds with nothing declared" "0" "$rc"
assert_eq "nothing is printed" "" "$out"

echo "== warn_advisory_files never fails when the daemon predates the flag =="
fresh_env advisory_predates
cat > "$DAEMON" <<'EOF'
#!/usr/bin/env bash
echo 'irrlichd: unknown flag "--print-advisory-files"' >&2
exit 2
EOF
chmod +x "$DAEMON"
out="$(warn_advisory_files "$DAEMON" 2>&1)"
rc=$?
assert_eq "warn_advisory_files never fails the recording" "0" "$rc"

echo "== warn_advisory_files under production's set -euo pipefail never kills the caller =="
# Reproduces the exact hazard at the real call site rather than this file's
# own `set -uo pipefail` (no -e, deliberately, so assertions can capture a
# non-zero return): both run-cell.sh and run-cell-multi.sh run under
# `set -euo pipefail`, and spawn_record_daemon calls warn_advisory_files as a
# BARE statement. A pipe-based implementation (`daemon | while read ...`) lets
# a non-zero daemon exit become the PIPELINE's own status, which errexit then
# treats as the whole script failing, right there — before this function's
# own `return 0` ever runs. $DAEMON here is still the exit-2 stub the case
# above just wrote.
subshell_out="$(bash -c '
  set -euo pipefail
  source "'"$DIR"'/managed-file-snapshot.sh"
  warn_advisory_files "'"$DAEMON"'"
  echo SURVIVED
' 2>&1)"
subshell_rc=$?
assert_eq "the calling script survives under set -euo pipefail" "0" "$subshell_rc"
assert_eq "execution continues past the bare call" "1" \
  "$(grep -c SURVIVED <<<"$subshell_out" | tr -d ' ')"

echo "== the lib names no agent config path of its own (#1357) =="
# The literal list is the defect. A path spelled out here is one the daemon
# does not have to agree with, which is how the two drifted in the first place.
LITERAL_RE='\.(claude|codex|gemini)/'
assert_eq "no agent config literals in the lib" "0" \
  "$(grep -cE "$LITERAL_RE" "$DIR/managed-file-snapshot.sh" | tr -d ' ')"

echo ""
if [[ "$fails" -eq 0 ]]; then
  echo "managed-file-snapshot_test: ALL PASS"
  exit 0
fi
echo "managed-file-snapshot_test: $fails FAILURE(S)" >&2
exit 1
