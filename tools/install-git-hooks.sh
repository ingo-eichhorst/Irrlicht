#!/usr/bin/env bash
# install-git-hooks.sh — symlink tools/git-hooks/* into .git/hooks/, so they
# run without a manual step every time. git does not version .git/hooks/, so
# this needs one run per clone (worktrees share the parent repo's .git/hooks
# automatically — no separate install needed there).
#
# Usage: tools/install-git-hooks.sh
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
HOOKS_DIR="$(git -C "$ROOT_DIR" rev-parse --git-common-dir)/hooks"

mkdir -p "$HOOKS_DIR"
for hook in "$ROOT_DIR"/tools/git-hooks/*; do
  name="$(basename "$hook")"
  ln -sf "$hook" "$HOOKS_DIR/$name"
  echo "installed $name -> $HOOKS_DIR/$name"
done
