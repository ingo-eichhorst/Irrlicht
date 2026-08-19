#!/usr/bin/env bash
# SC2155 — `local x=$(cmd)` masks cmd's return value: the declaration always
# succeeds, so a failing command is invisible to `set -e` and to `$?`.
set -euo pipefail

report() {
  local out=$(false)
  printf '%s\n' "$out"
}
report
