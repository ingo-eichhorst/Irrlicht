#!/usr/bin/env bash
# slots.sh — shared multi-session slot bookkeeping for the interactive
# recording drivers (#508 #3). Extracted from drive-codex-interactive.sh and
# drive-pi-interactive.sh, whose slot model was byte-identical except the
# per-slot discovery-marker filename. claudecode uses a DIFFERENT slot variable
# scheme and does NOT source this; opencode is headless and has no slots.
#
# The sourcing driver owns these globals — this lib only reads/writes them:
#   View vars  : SESSION TRANSCRIPT UUID EXPECTED_TURNS MARKER ACTIVE
#   Slot arrays: SES_SESSION SES_TRANSCRIPT SES_UUID SES_EXPECTED SES_MARKER
#                SES_CWD SES_ALIVE   (1-based; index 0 unused)
#   Counter    : N_SLOTS
#   Paths      : STAGING, plus DRIVE_MARKER_PREFIX (per-slot marker path stem;
#                defaults to "$STAGING/.start-marker" when unset)
#
# Sourced as a library; MUST NOT call `set` at top level.

# daemon_sid maps an absolute transcript path to the daemon's session_id
# (basename minus ".jsonl") — the filename-stem form the fswatcher keys on
# (see extractSessionID) and curate-lifecycle-fixture.sh filters by
# `.session_id`, so fixture lists MUST hold this form, not the bare payload id.
daemon_sid() {
  local p="$1"
  [[ -z "$p" ]] && { echo ""; return; }
  local b; b="$(basename "$p")"
  echo "${b%.jsonl}"
}

# save_active persists the active-view variables back into the active slot.
save_active() {
  [[ $ACTIVE -ge 1 ]] || return 0
  SES_SESSION[$ACTIVE]="$SESSION"
  SES_TRANSCRIPT[$ACTIVE]="$TRANSCRIPT"
  SES_UUID[$ACTIVE]="$UUID"
  SES_EXPECTED[$ACTIVE]="$EXPECTED_TURNS"
  SES_MARKER[$ACTIVE]="$MARKER"
}

# load_slot makes slot $1 the active session and loads its state into the view.
load_slot() {
  ACTIVE="$1"
  SESSION="${SES_SESSION[$ACTIVE]}"
  TRANSCRIPT="${SES_TRANSCRIPT[$ACTIVE]}"
  UUID="${SES_UUID[$ACTIVE]}"
  EXPECTED_TURNS="${SES_EXPECTED[$ACTIVE]}"
  MARKER="${SES_MARKER[$ACTIVE]}"
  return 0
}

# alloc_slot allocates a fresh slot (tmux session name $1, cwd $2), mints a
# per-slot discovery marker, and makes it active with cleared TRANSCRIPT/UUID/
# EXPECTED_TURNS so the new session starts known.
alloc_slot() {
  local sess="$1" cwd="$2"
  N_SLOTS=$((N_SLOTS + 1))
  local marker="${DRIVE_MARKER_PREFIX:-$STAGING/.start-marker}.$N_SLOTS"
  touch "$marker"
  SES_SESSION[$N_SLOTS]="$sess"
  SES_TRANSCRIPT[$N_SLOTS]=""
  SES_UUID[$N_SLOTS]=""
  SES_EXPECTED[$N_SLOTS]=0
  SES_MARKER[$N_SLOTS]="$marker"
  SES_CWD[$N_SLOTS]="$cwd"
  SES_ALIVE[$N_SLOTS]=1
  ACTIVE=$N_SLOTS
  SESSION="$sess"
  TRANSCRIPT=""
  UUID=""
  EXPECTED_TURNS=0
  MARKER="$marker"
  return 0
}
