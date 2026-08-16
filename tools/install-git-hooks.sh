#!/usr/bin/env bash
# install-git-hooks.sh — install tools/git-hooks/* into .git/hooks/, so they
# run without a manual step every time. git does not version .git/hooks/, so
# this needs one run per clone (worktrees share the parent repo's .git/hooks
# automatically — no separate install needed there).
#
# What lands in .git/hooks/<name> is NEITHER the hook script NOR a symlink to
# it: it is tools/git-hooks/shim, copied verbatim under each hook's name. The
# shim resolves the PUSHING working tree at run time and execs that tree's own
# tools/git-hooks/<name>. Read that file for the full reasoning; in one line
# (#1591): worktrees share this hooks directory, so a symlink pinned every
# worktree to the MAIN checkout's script and a hook change in a worktree did
# not govern that worktree's own push.
#
# Consequence, stated rather than left implied: the SHIM is now the one thing
# a `git pull` cannot update, so re-run this script after tools/git-hooks/shim
# changes. It overwrites whatever it finds — an older symlink install, a
# hand-edited copy, a stale shim — so re-running is always safe and always
# sufficient, and a second run in a row installs nothing.
#
# Usage: tools/install-git-hooks.sh
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"

# `git rev-parse --git-common-dir` answers RELATIVE to the caller's cwd (plain
# `.git` from the top of the main checkout), so it has to be re-anchored on
# ROOT_DIR or a run from anywhere else creates `./.git/hooks` in the wrong
# place and installs nothing where git will look. Re-anchoring by hand rather
# than with --path-format=absolute keeps this working on git < 2.31.
COMMON_DIR="$(git -C "$ROOT_DIR" rev-parse --git-common-dir)"
case "$COMMON_DIR" in
  /*) ;;
  *) COMMON_DIR="$ROOT_DIR/$COMMON_DIR" ;;
esac
HOOKS_DIR="$COMMON_DIR/hooks"
SHIM="$ROOT_DIR/tools/git-hooks/shim"

if [[ ! -f "$SHIM" ]]; then
  echo "install-git-hooks: $SHIM is missing — refusing to install hooks that would have nothing to delegate through." >&2
  exit 1
fi

mkdir -p "$HOOKS_DIR"
for hook in "$ROOT_DIR"/tools/git-hooks/*; do
  name="$(basename "$hook")"
  # The shim is this installer's payload, not a hook. git would never run a
  # file by that name, but installing it would still be noise in a directory
  # every worktree shares.
  [[ "$name" == "shim" ]] && continue

  target="$HOOKS_DIR/$name"
  # Idempotent: a target that is already a regular file with the shim's exact
  # bytes is left alone. `! -L` matters — cmp follows a symlink, so a link
  # pointing AT the shim would otherwise compare equal and never be replaced
  # by a real copy.
  if [[ -f "$target" && ! -L "$target" ]] && cmp -s "$SHIM" "$target"; then
    echo "up to date  $name  ($target)"
    continue
  fi

  # rm before cp: `cp` onto a symlink writes THROUGH it, and the install this
  # replaces left exactly such a symlink — pointing at the main checkout's
  # tools/git-hooks/<name>. Without the rm, upgrading an old clone would
  # overwrite the tracked hook script with the shim.
  rm -f "$target"
  cp "$SHIM" "$target"
  chmod +x "$target"
  echo "installed   $name  ($target -> shim -> <pushing worktree>/tools/git-hooks/$name)"
done
