#!/usr/bin/env bash
# THE VACUITY GUARD. Clean bash, and it carries as much weight as every
# bad-*.sh beside it: a linter that simply failed everything would satisfy
# each of those and be worthless, and only this file can tell the two apart.
set -euo pipefail

greet() {
  local who="$1"
  printf 'hello, %s\n' "$who"
}

target="$(mktemp -d)"
trap 'rm -rf "${target:?}"' EXIT
greet world
cd "$target" || exit 1
printf 'done\n'
