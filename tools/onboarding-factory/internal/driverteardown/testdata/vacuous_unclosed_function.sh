#!/usr/bin/env bash
# VACUITY — a function body that is never closed by a `}` at column 0. Read
# best-effort, the region would swallow the rest of the file and move the
# top-level end-of-run sweep INSIDE a function, where INV-1 does not grade it.
# A validator that cannot parse its input checks MORE, never less: this ERRORs.
set -euo pipefail
SESSION="fixture-onboard-$(date +%s)-$$"

cleanup() {
  tmux kill-session -t "$SESSION" 2>/dev/null || true
trap cleanup EXIT

tmux new-session -d -s "$SESSION" "fixture-agent"
