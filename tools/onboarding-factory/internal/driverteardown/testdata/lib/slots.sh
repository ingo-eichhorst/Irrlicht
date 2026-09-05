#!/usr/bin/env bash
# Fixture stand-in for replaydata/_lib/drive/slots.sh. Only the part the
# checker's dataflow walks is kept: alloc_slot takes the tmux session NAME as
# its FIRST argument and lands it on both the slot array and the view var.
# Nothing here is executed — the fixtures are read, never run.

alloc_slot() {
  local sess="$1" cwd="$2"
  N_SLOTS=$((N_SLOTS + 1))
  SES_SESSION[$N_SLOTS]="$sess"
  SES_CWD[$N_SLOTS]="$cwd"
  SES_OWNED[$N_SLOTS]=1
  ACTIVE=$N_SLOTS
  SESSION="$sess"
}
