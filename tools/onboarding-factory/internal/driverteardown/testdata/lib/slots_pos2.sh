#!/usr/bin/env bash
# Fixture stand-in for claudecode's PRIVATE slot scheme, where alloc_slot's
# signature is (uuid, tmux-name, cwd) — the name is the SECOND argument, not
# the first. It exists so the INV-3 dataflow is proved to DERIVE the
# name-carrying argument position rather than assuming position 1.

alloc_slot() {
  N_SLOTS=$((N_SLOTS + 1))
  SES_UUID[$N_SLOTS]="$1"
  SES_TMUX[$N_SLOTS]="$2"
  SES_CWD[$N_SLOTS]="$3"
  ACTIVE=$N_SLOTS
  CURRENT_UUID="$1"
  CURRENT_TMUX="$2"
  CURRENT_CWD="$3"
}
