#!/usr/bin/env bash
# managed-file-snapshot.sh — save and restore the shared user-owned files a
# recording daemon writes, so a recording never outlives its own edits.
#
# A recording daemon runs with IRRLICHT_PERMISSION_MODE=grant-all, which grants
# EVERY consent-gated capability and therefore runs every modify permission's
# Apply closure against the user's REAL $HOME. Those paths follow $HOME, not
# IRRLICHT_HOME, so IRRLICHT_ONBOARD_HOME does not isolate them. Two distinct
# hazards, and both are why nothing here is optional:
#
#   - Broken state. Since #1178 the hook endpoint written into an agent config
#     carries the daemon's own bind port, so a coexist recording on :7838
#     repoints a running production daemon's hooks at the recorder and leaves
#     them that way.
#   - Consent violation. grant-all installs content the user may have
#     explicitly denied — irrlicht's managed instruction blocks in their own
#     CLAUDE.md, `allow_remote_control yes` in their own kitty.conf (#1383).
#     Sentinel-delimited and idempotent, so not broken state, but still content
#     they did not agree to and did not ask to keep.
#
# WHICH files those are is asked of the daemon binary itself
# (`irrlichd --print-managed-files`), which resolves them from the consent
# catalog — the same declarations its own `--uninstall-hooks` reads a hooks-only
# slice of (#1357, #1383). A literal list here would be correct only for as long
# as the catalog stayed frozen: recording a file-writing permission this file
# had never heard of would leave the user carrying its edits, which is #1214 one
# declaration later. There is deliberately no fallback list — a daemon that
# cannot answer fails the snapshot instead.
#
# Usage — call the first before spawning the daemon, the second from cleanup:
#   source "$SCRIPT_DIR/lib/managed-file-snapshot.sh"
#   snapshot_managed_files "$STAGING/managed-file-backup" "$DAEMON" || exit 1
#   warn_advisory_files "$DAEMON"   # optional: report-only, see below
#   ... spawn daemon, drive agent ...
#   restore_managed_files
#
# restore_managed_files is a no-op unless a snapshot completed, so it is safe to
# call unconditionally from an EXIT trap.
#
# warn_advisory_files is a THIRD, separate category (issue #1748):
# `irrlichd --print-advisory-files` names paths an Apply is KNOWN to write
# that hold no irrlicht content and cannot be safely snapshotted/restored (an
# agent CLI's own store — kiro-cli's identity database is the one declared
# case). Never backed up, never restored — see the function's own doc for why
# that is deliberate rather than a gap in this one.

# MANAGED_FILE_BACKUP_DIR is the only state the pair shares, and it is set ONLY
# by a snapshot that ran to completion. Everything restore does comes out of the
# manifest inside it — there is deliberately no published file list, because two
# things that can disagree about which files are protected is the defect this
# lib is fixing, not a shape to reproduce in miniature.
MANAGED_FILE_BACKUP_DIR=""
MANAGED_FILE_SEAL_DIR=""

# managed_file_paths <daemon-bin> prints one absolute agent config path per line,
# as declared by the daemon's adapter registry.
#
# Two deliberate details. It writes to a caller-supplied FILE rather than to
# stdout, so it is never run in a command substitution — that pipe is not closed
# until every process holding it exits, which a backgrounded watchdog inherits.
# And stderr is captured separately rather than merged: a diagnostic line that
# happened to look like a path would otherwise be adopted as a file to manage,
# and at restore anything sitting there would be moved out of the user's
# filesystem.
#
# The bound exists because an irrlichd predating the flag does not reject it —
# `hasFlag` dispatch ignores what it does not recognize and falls through to
# starting a full production daemon, which would otherwise block here forever.
# Defense in depth rather than the primary guarantee: precheck.sh rebuilds the
# daemon from the current worktree before every run-cell.sh / run-cell-multi.sh
# run, so the two production callers cannot reach a stale binary.
managed_file_paths() {
  local daemon_bin="$1" outf="$2" secs="${MANAGED_FILE_PROBE_TIMEOUT_S:-20}"
  local errf rc pid watchdog
  errf="$outf.err"

  "$daemon_bin" --print-managed-files >"$outf" 2>"$errf" &
  pid=$!
  { sleep "$secs"; kill -KILL "$pid" 2>/dev/null; } >/dev/null 2>&1 &
  watchdog=$!
  wait "$pid"; rc=$?
  kill "$watchdog" 2>/dev/null
  wait "$watchdog" 2>/dev/null

  if [[ "$rc" -ne 0 ]]; then
    echo "managed-file-snapshot: '$daemon_bin --print-managed-files' failed (exit $rc): $(head -c 500 "$errf")" >&2
    echo "managed-file-snapshot: an irrlichd predating the flag starts a daemon instead of answering; refusing to guess the file list" >&2
    rm -f "$errf"
    return 1
  fi
  if [[ ! -s "$outf" ]]; then
    echo "managed-file-snapshot: '$daemon_bin --print-managed-files' printed nothing" >&2
    rm -f "$errf"
    return 1
  fi
  rm -f "$errf"
  return 0
}

# record_absent_dirs <manifest> <slot> <path> appends one 'absentdir' line per
# ancestor directory of <path> that does not exist yet, deepest first.
#
# A declared file's Apply closure creates its parent directories on the way to
# writing it, and moving the file back out at teardown leaves them behind. For
# most paths that residue is inert; for kitty it is not. `KittyDetected` returns
# true on the mere EXISTENCE of the config directory (it stats the dir before
# falling back to pgrep), and a recording daemon under grant-all runs every
# Apply whether or not the user has kitty — so one recording on a kitty-less
# machine would leave an empty ~/.config/kitty behind and the user's production
# daemon would offer the kitty permission in the wizard from then on. That is a
# recording outliving its own edits, which is the whole point of this lib
# (#1383).
#
# The walk stops at the first ancestor that already exists, so nothing a
# pre-existing tree contains is ever recorded, and at the filesystem root, so it
# cannot run away on a path with no existing ancestor at all.
record_absent_dirs() {
  local manifest="$1" slot="$2" path="$3" dir parent
  dir="$(dirname "$path")"
  while [[ -n "$dir" && "$dir" != "/" && "$dir" != "." && ! -d "$dir" ]]; do
    printf 'absentdir\t%s\t%s\n' "$slot" "$dir" >> "$manifest"
    parent="$(dirname "$dir")"
    [[ "$parent" == "$dir" ]] && break
    dir="$parent"
  done
  return 0
}

# snapshot_managed_files <backup-dir> <daemon-bin> copies each declared agent
# config aside and records, per file, whether it existed. Returns non-zero
# without publishing any state if the daemon cannot be asked, if a path looks
# wrong, or if any backup fails to land — the caller must not spawn a recording
# daemon it cannot undo.
snapshot_managed_files() {
  local backup_dir="${1:-}" daemon_bin="${2:-}"

  if [[ -z "$backup_dir" || -z "$daemon_bin" ]]; then
    echo "managed-file-snapshot: usage: snapshot_managed_files <backup-dir> <daemon-bin>" >&2
    return 1
  fi
  # Nothing here clears MANAGED_FILE_BACKUP_DIR on the way IN, and a second
  # snapshot over a live one is refused rather than allowed to replace it. A
  # failing re-snapshot that had already cleared it would silently disarm
  # restore for the snapshot that DID succeed, while the EXIT trap that calls it
  # stayed armed and did nothing. A caller that genuinely wants to start over
  # clears MANAGED_FILE_BACKUP_DIR itself.
  if [[ -n "$MANAGED_FILE_BACKUP_DIR" ]]; then
    echo "managed-file-snapshot: a snapshot is already active at $MANAGED_FILE_BACKUP_DIR; restore it before taking another" >&2
    return 1
  fi

  if ! mkdir -p "$backup_dir"; then
    echo "managed-file-snapshot: cannot create the backup dir: $backup_dir" >&2
    return 1
  fi

  local listing="$backup_dir/declared"
  managed_file_paths "$daemon_bin" "$listing" || { rm -f "$listing"; return 1; }

  local paths=() p
  while IFS= read -r p; do
    [[ -n "$p" ]] || continue
    if [[ "$p" != /* ]]; then
      echo "managed-file-snapshot: refusing a non-absolute config path: $p" >&2
      return 1
    fi
    # The manifest is tab-separated and line-oriented; a path carrying either
    # would be silently mis-parsed on the way back out.
    if [[ "$p" == *$'\t'* ]]; then
      echo "managed-file-snapshot: refusing a config path containing a tab: $p" >&2
      return 1
    fi
    paths+=("$p")
  done < "$listing"

  if [[ "${#paths[@]}" -eq 0 ]]; then
    echo "managed-file-snapshot: '$daemon_bin --print-managed-files' listed no paths" >&2
    return 1
  fi

  local manifest_tmp="$backup_dir/manifest.tmp"
  rm -f "$backup_dir/manifest" "$manifest_tmp"
  if ! : > "$manifest_tmp"; then
    echo "managed-file-snapshot: cannot write $manifest_tmp" >&2
    return 1
  fi

  local i=0
  for p in "${paths[@]}"; do
    if [[ -f "$p" ]]; then
      # Both halves matter: cp can fail outright (unwritable dir), and it can
      # "succeed" while landing somewhere else (a directory already sitting on
      # the backup path swallows the file as dir/<basename>). Either way the
      # backup is not where restore will look for it, and the pre-#1357 code
      # read that as "the file never existed" and deleted the user's config.
      if ! cp "$p" "$backup_dir/$i" || [[ ! -f "$backup_dir/$i" ]]; then
        echo "managed-file-snapshot: cannot back up $p to $backup_dir/$i" >&2
        rm -f "$manifest_tmp"
        return 1
      fi
      printf 'saved\t%s\t%s\n' "$i" "$p" >> "$manifest_tmp"
    else
      printf 'absent\t%s\t%s\n' "$i" "$p" >> "$manifest_tmp"
      # AFTER the file's own line, deepest-first, so restore moves the file out
      # before trying to remove the directory holding it.
      record_absent_dirs "$manifest_tmp" "$i" "$p"
    fi
    i=$((i + 1))
  done

  if ! mv "$manifest_tmp" "$backup_dir/manifest"; then
    echo "managed-file-snapshot: cannot finalize $backup_dir/manifest" >&2
    rm -f "$manifest_tmp"
    return 1
  fi

  MANAGED_FILE_BACKUP_DIR="$backup_dir"
  MANAGED_FILE_SEAL_DIR=""
  return 0
}

# seal_managed_files records the exact post-install state that belongs to the
# recording daemon. A strict caller invokes this after the hook-install wait.
# Restore then distinguishes this expected change from an edit made by another
# process during the live Desktop run. A mismatch stops before any old bytes
# are copied back.
seal_managed_files() {
  [[ -n "$MANAGED_FILE_BACKUP_DIR" ]] || {
    echo "managed-file-snapshot: cannot seal without an active snapshot" >&2
    return 1
  }
  local manifest="$MANAGED_FILE_BACKUP_DIR/manifest"
  [[ -f "$manifest" ]] || {
    echo "managed-file-snapshot: cannot seal without $manifest" >&2
    return 1
  }
  local seal="$MANAGED_FILE_BACKUP_DIR/expected" tmp="$MANAGED_FILE_BACKUP_DIR/expected.tmp"
  rm -rf "$seal"
  mkdir -p "$seal" || return 1
  : > "$tmp" || return 1

  local state slot path
  while IFS=$'\t' read -r state slot path; do
    case "$state" in
      saved | absent)
        if [[ -f "$path" ]]; then
          cp "$path" "$seal/$slot" || {
            echo "managed-file-snapshot: cannot seal expected bytes for $path" >&2
            rm -f "$tmp"
            return 1
          }
          printf 'file\t%s\t%s\n' "$slot" "$path" >> "$tmp"
        elif [[ -e "$path" ]]; then
          echo "managed-file-snapshot: cannot seal non-regular config path $path" >&2
          rm -f "$tmp"
          return 1
        else
          printf 'absent\t%s\t%s\n' "$slot" "$path" >> "$tmp"
        fi
        ;;
      absentdir) ;;
      *)
        echo "managed-file-snapshot: cannot seal unknown manifest state '$state'" >&2
        rm -f "$tmp"
        return 1
        ;;
    esac
  done < "$manifest"
  mv "$tmp" "$seal/manifest" || return 1
  MANAGED_FILE_SEAL_DIR="$seal"
  return 0
}

# verify_managed_file_seal is a read-only all-files precondition. It returns
# before restore changes any path if a daemon-owned config no longer matches
# the post-install seal.
verify_managed_file_seal() {
  [[ -n "$MANAGED_FILE_SEAL_DIR" ]] || return 0
  local manifest="$MANAGED_FILE_SEAL_DIR/manifest"
  [[ -f "$manifest" ]] || {
    echo "managed-file-snapshot: expected-state seal is missing at $manifest" >&2
    return 1
  }
  local state slot path
  while IFS=$'\t' read -r state slot path; do
    case "$state" in
      file)
        if [[ ! -f "$path" ]] || ! cmp -s "$path" "$MANAGED_FILE_SEAL_DIR/$slot"; then
          echo "managed-file-snapshot: refusing to overwrite concurrent change at $path" >&2
          return 1
        fi
        ;;
      absent)
        if [[ -e "$path" ]]; then
          echo "managed-file-snapshot: refusing to overwrite concurrent creation at $path" >&2
          return 1
        fi
        ;;
      *)
        echo "managed-file-snapshot: invalid expected-state seal '$state' for $path" >&2
        return 1
        ;;
    esac
  done < "$manifest"
  return 0
}

# warn_advisory_files <daemon-bin> prints one WARN line per advisory path
# (`irrlichd --print-advisory-files`, issue #1748) — a path an Apply is KNOWN
# to write that holds no irrlicht content and cannot be safely snapshotted or
# restored (kiro-cli's own agent-identity sqlite store is the motivating,
# and today only, case). Unlike snapshot_managed_files above, this is
# VISIBILITY ONLY: nothing here is backed up, nothing here is restored — the
# whole point is telling the operator a recording touched a store this rig
# cannot safely undo, not pretending the rig can undo it.
#
# Best-effort and NEVER fails the recording, on purpose: an irrlichd predating
# the flag is rejected by `hasFlag` and prints nothing to stdout (the same
# fallthrough managed_file_paths' own doc names), and a catalog with no
# advisory declarations at all is the ordinary case, not a fault. Either way
# this returns 0 — including when the daemon exits non-zero or hangs.
#
# Deliberately NOT `"$daemon_bin" ... | while read ...`: both real callers
# (run-cell.sh, run-cell-multi.sh) run under `set -euo pipefail`, and this is
# invoked as a bare statement in spawn_record_daemon — under `pipefail` a
# non-zero daemon exit becomes the PIPELINE's exit status, and under the
# inherited `errexit` that aborts the whole calling script right there,
# before this function's own `return 0` ever runs (measured: a stub daemon
# exiting 2 killed the caller under bash 3.2, this repo's floor). Redirecting
# to a file and reading it back afterward, exactly like managed_file_paths
# above, keeps this function's own exit status the only thing that reaches
# the caller.
#
# Every `wait`/`kill` below is ALSO guarded with its own `|| true` /
# `|| rc=$?`, not just the pipe removed above. `wait "$pid"; rc=$?` alone is
# the SAME hazard one line down: under `errexit`, a bare `wait` returning the
# backgrounded process's non-zero status aborts the caller right there, same
# as the pipe did — it only happens NOT to fire for managed_file_paths' own
# copy of this pattern because that function's one call site is the left side
# of `||` in snapshot_managed_files, which (per bash's documented -e
# semantics) suspends errexit for that whole function body as a side effect
# of the CALL site, not a property of the function itself. This function's
# own doc promises "never fails the recording" regardless of how it is
# invoked, so it does not rely on being called that way — every internal
# command that can return non-zero is guarded here directly (measured: this
# was caught by this file's own committed regression test, which drives the
# function under `set -euo pipefail` as a bare statement, matching
# spawn_record_daemon's actual call site).
warn_advisory_files() {
  local daemon_bin="$1" secs="${MANAGED_FILE_PROBE_TIMEOUT_S:-20}"
  local outf rc pid watchdog path reason
  outf="$(mktemp)" || return 0

  "$daemon_bin" --print-advisory-files >"$outf" 2>/dev/null &
  pid=$!
  { sleep "$secs"; kill -KILL "$pid" 2>/dev/null || true; } >/dev/null 2>&1 &
  watchdog=$!
  rc=0
  wait "$pid" || rc=$?
  kill "$watchdog" 2>/dev/null || true
  wait "$watchdog" 2>/dev/null || true

  if [[ "$rc" -ne 0 ]]; then
    rm -f "$outf"
    return 0
  fi

  while IFS=$'\t' read -r path reason; do
    [[ -n "$path" ]] || continue
    echo "managed-file-snapshot: ADVISORY — this recording's grant-all daemon is expected to write $path ($reason); not backed up, not restored (#1748)" >&2
  done < "$outf"
  rm -f "$outf"
  return 0
}

# preserve_replaced <slot> <path> keeps a copy of whatever is at <path> now,
# when it differs from the backup that is about to overwrite it.
#
# The `absent` branch below already refuses to destroy a file the AGENT created
# mid-run, on the grounds that a shared config is not irrlicht's to delete. A
# file that EXISTED before the run can gain exactly the same kind of content —
# and since #1383 widened the protected set, one of them is the user's own
# CLAUDE.md — prose written by the very agent the rig drives. Restoring the pre-run
# bytes is still what the snapshot promises, so <path> ends up identical either
# way; this only stops the pre-run copy from being the ONLY copy left.
#
# Symmetric with created/, and deliberately quiet when the file is unchanged:
# the common case is a config the daemon rewrote with content we are discarding
# on purpose, and a stderr line per file would bury the one that matters.
preserve_replaced() {
  local slot="$1" path="$2" dir="$MANAGED_FILE_BACKUP_DIR/replaced"
  [[ -f "$path" ]] || return 0
  cmp -s "$path" "$MANAGED_FILE_BACKUP_DIR/$slot" && return 0
  if mkdir -p "$dir" && cp "$path" "$dir/$slot"; then
    echo "managed-file-snapshot: $path changed during the run; its run-time version is kept at $dir/$slot" >&2
  else
    echo "managed-file-snapshot: could not keep a copy of $path before restoring it" >&2
  fi
  return 0
}

# restore_managed_files puts each snapshotted file back exactly as it was.
# Every action is driven by the manifest the snapshot wrote: "saved" means copy
# the backup back, "absent" means the file did not exist before the run, and
# "absentdir" is a directory the run created on the way to writing an absent
# file. A file is never removed on the strength of a MISSING record — that
# inference is what made widening the file list dangerous, because a config the
# snapshot failed to save reads identically to one that was never there (#1357).
restore_managed_files() {
  [[ -n "$MANAGED_FILE_BACKUP_DIR" ]] || return 0
  local manifest="$MANAGED_FILE_BACKUP_DIR/manifest"
  if [[ ! -f "$manifest" ]]; then
    echo "managed-file-snapshot: no manifest at $manifest — leaving every agent config alone" >&2
    return 1
  fi

  verify_managed_file_seal || return 1

  local created_dir="$MANAGED_FILE_BACKUP_DIR/created"
  local state slot path restore_rc=0
  while IFS=$'\t' read -r state slot path; do
    if [[ -n "$path" ]]; then
      case "$state" in
        saved)
          if [[ -f "$MANAGED_FILE_BACKUP_DIR/$slot" ]]; then
            preserve_replaced "$slot" "$path"
            if ! cp "$MANAGED_FILE_BACKUP_DIR/$slot" "$path"; then
              echo "managed-file-snapshot: could not restore $path" >&2
              restore_rc=1
            fi
          else
            echo "managed-file-snapshot: backup for $path is gone — leaving the file as it is" >&2
            restore_rc=1
          fi
          ;;
        absent)
          # Removing it restores the pre-run state, but the file may be a
          # multi-purpose config the AGENT (not the daemon) created during the
          # run, so move it aside rather than destroying it. The path ends up
          # absent either way; the contents stay recoverable.
          #
          # -f, symmetric with the snapshot's own test, NOT -e: the snapshot
          # records anything that is not a regular file as "absent", so an -e
          # here would relocate a whole DIRECTORY a user happens to keep at a
          # declared config path — worse than the rm -f this replaced, which
          # refuses a directory outright.
          if [[ -e "$path" && ! -f "$path" ]]; then
            echo "managed-file-snapshot: $path is not a regular file; leaving it alone" >&2
          elif [[ -f "$path" ]]; then
            if mkdir -p "$created_dir" && mv "$path" "$created_dir/$slot"; then
              echo "managed-file-snapshot: $path did not exist before the run; moved to $created_dir/$slot" >&2
            else
              echo "managed-file-snapshot: could not move $path aside" >&2
              restore_rc=1
            fi
          fi
          ;;
        absentdir)
          # A directory the run created on the way to writing a file that was
          # absent. rmdir REFUSES a non-empty directory, so one that has since
          # gained content of its own is left alone by construction — there is
          # no -e/-f inference to get wrong here, and a failure is the correct
          # outcome rather than an error to report.
          if rmdir "$path" 2>/dev/null; then
            echo "managed-file-snapshot: removed $path, created during the run" >&2
          fi
          ;;
        *)
          echo "managed-file-snapshot: unrecognized manifest state '$state' for $path — leaving it alone" >&2
          restore_rc=1
          ;;
      esac
    fi
  done < "$manifest"

  # The snapshot is spent: clear the shared state so a second restore is a
  # no-op and a caller that legitimately records again can take a fresh one.
  MANAGED_FILE_BACKUP_DIR=""
  MANAGED_FILE_SEAL_DIR=""
  return "$restore_rc"
}
