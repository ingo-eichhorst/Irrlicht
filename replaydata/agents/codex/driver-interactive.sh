#!/usr/bin/env bash
# drive-codex-interactive.sh — drive codex's REPL via tmux, executing a
# step-script (send / wait_turn / interrupt / slash / …). For scenarios
# that can't be expressed as a single `codex exec ...` invocation:
# multi-turn conversations, mid-turn interrupts, /new and /fork
# session swaps, resume relaunches, and multiple concurrent sessions.
#
# Sister script to drive-codex.sh (headless `codex exec` mode). Same
# staging contract: writes driver.log[.stdout], driver.exit-reason,
# transcript.path, session.uuid — PLUS the multi-session lists
# session.uuids / transcript.paths (newline-separated, in order) so
# run-cell.sh / curate union ALL sessions a single run produces into
# the fixture. Mirrors drive-claudecode-interactive.sh's multi-session
# contract, adapted to codex's rollout-discovery model.
#
# Step types match the aider/claudecode/pi interactive drivers:
#   send       — type text, press Enter
#   slash      — same as send, used for /commands
#   wait_turn  — block until a new {"type":"event_msg","payload":{
#                "type":"task_complete",...}} line appears in the rollout
#                (signals codex finished the LLM round)
#   interrupt  — Escape the in-flight turn; codex emits
#                event_msg/turn_aborted instead of task_complete, so the
#                task_complete-only turn-counter naturally skips it
#   keys       — raw tmux key sequence (Up/Down/Enter/Escape …) for
#                navigating picker UIs such as /model
#   sleep      — pause N seconds (field: "seconds")
#   reset_session — send /new; codex abandons the current conversation
#                and writes the NEXT prompt's turns to a brand-new rollout
#                (new session_id) — the fresh session supersedes the old.
#                The driver records the old session and re-discovers the
#                new rollout on the next wait_turn.
#   fork       — send /fork; codex clones the conversation into a new
#                thread with a fresh session_id. Same new-rollout
#                discovery as reset_session.
#   exit_clean — Ctrl-D for a graceful shutdown (codex flushes its
#                rollout and the daemon emits process_exited).
#   resume     — Ctrl-D the current codex, kill the tmux session, then
#                relaunch `codex resume <UUID> --no-alt-screen`. Codex
#                APPENDS to the SAME rollout (same session_id) across the
#                two process lifetimes (verified empirically), so the
#                session identity is kept unchanged — no new slot is
#                allocated for the resumed half.
#   sigkill    — kill -9 the active session's codex PID (abrupt teardown,
#                no rollout flush). Codex argv is uniform, so we can't
#                pgrep by session like claudecode does with --session-id;
#                instead we target the daemon's PID directly — the process
#                holding the rollout open for writing (see codex/pid.go).
#   restart    — end the active session, start a FRESH one (new rollout,
#                new session_id, fresh cwd). Mirrors the claudecode
#                driver's restart; used to separate session-end variants.
#
# Concurrency (multiple live sessions at once):
#   start_session — launch a NEW codex session WITHOUT tearing down the
#                   active one. Defaults to the same cwd as session 1
#                   (the multiple-sessions-same-cwd case); codex caches
#                   trust per directory so no second trust dialog fires.
#                   Override with {"type":"start_session","cwd":"…"}.
#   any step may carry {"session": N} to switch the active context to
#   session slot N (1-based) before executing — e.g. send a turn to
#   session 1 after start_session moved focus to session 2. A bare
#   {"type":"session","session":N} just switches focus.
#
# Session model: every session lifetime is a 1-based "slot". The initial
# session is slot 1. reset_session/fork/start_session each allocate the
# next slot; reset_session/fork reuse the active codex process (rotate
# its rollout) and retire the old slot, start_session launches a fresh
# process and leaves the previous slot alive. resume relaunches in place
# (same slot, same rollout). At the end, ALL slots' session_ids +
# transcripts are written to session.uuids / transcript.paths so
# run-cell.sh's multi-session curation unions them.
#
# Codex assigns its OWN session UUID per rollout and has no --session-id
# flag; both args are accepted for ABI parity with the other interactive
# drivers. A shared workspace can be forced via $IRRLICHT_ONBOARD_CWD
# (used by run-cell.sh's cross-adapter mode); otherwise each run uses an
# isolated per-run cwd under the staging dir.
#
# Usage:
#   drive-codex-interactive.sh <staging-dir> <preferred-uuid-ignored> \
#       <timeout-seconds> <settings-path-ignored> <script-json>

set -euo pipefail

if [[ $# -ne 5 ]]; then
  echo "usage: drive-codex-interactive.sh <staging> <uuid> <timeout-s> <settings-path> <script-json>" >&2
  exit 2
fi

STAGING="$1"
# $2 (preferred-uuid) is accepted for ABI parity with the other interactive
# drivers; codex assigns its own UUID, so it is unused here. $4 (settings
# path) IS read: codex has no --settings flag, but the blob carries this
# driver's launch_args (see CODEX_LAUNCH_ARGS below).
TIMEOUT_S="$3"
SETTINGS_PATH="$4"
SCRIPT_JSON="$5"

mkdir -p "$STAGING"
DRIVER_LOG="$STAGING/driver.log"

# Codex resolves its entire config dir — sessions included — from CODEX_HOME,
# and so does the DAEMON (codexHome() in
# core/adapters/inbound/agents/codex/hookinstaller.go, which is also what
# `irrlichd --print-managed-files` reports and therefore what the recorder
# snapshots). Hardcoding $HOME here split the two halves apart: with
# CODEX_HOME set, codex wrote rollouts into the isolated home while this
# driver hunted them under the real one, so resolve_transcript found nothing
# and every cell died at readiness_timeout. Resolve it the same way the
# daemon does, so an isolated recording home leaves the user's real ~/.codex
# untouched (#1388).
#
# Mirror codexHome()'s rule EXACTLY, including that it honors the override
# only when it is ABSOLUTE and otherwise falls back to $HOME/.codex. A
# `${CODEX_HOME:-…}` default would accept a relative value that the daemon
# silently ignores, re-creating the very split this resolves — so refuse it
# loudly instead of resolving differently from the daemon.
CODEX_HOME_RESOLVED="$HOME/.codex"
if [[ -n "${CODEX_HOME:-}" ]]; then
  if [[ "$CODEX_HOME" != /* ]]; then
    echo "[driver] CODEX_HOME must be absolute — the daemon's codexHome() ignores" >&2
    echo "[driver] relative values, so the driver and daemon would use different" >&2
    echo "[driver] homes. Got: '$CODEX_HOME'" >&2
    exit 2
  fi
  CODEX_HOME_RESOLVED="$CODEX_HOME"
fi
CODEX_SESSIONS_DIR="$CODEX_HOME_RESOLVED/sessions"
mkdir -p "$CODEX_SESSIONS_DIR"

# codex records its hook-trust keys under the PHYSICAL path — on macOS /tmp is
# a symlink to /private/tmp, so a home under /tmp is written as
# [hooks.state."/private/tmp/…/hooks.json:stop:0:0"]. Resolve ours the same way
# or the trust lookup at the end of boot_session reports a false "NOT trusted"
# for an install that is perfectly trusted (observed on a run whose hooks did
# fire). A false alarm here is nearly as costly as the false reassurance it
# replaced, so keep both spellings and match either.
CODEX_HOME_PHYS="$(cd "$CODEX_HOME_RESOLVED" 2>/dev/null && pwd -P)"
CODEX_HOME_PHYS="${CODEX_HOME_PHYS:-$CODEX_HOME_RESOLVED}"

# Extra argv for the `codex` boot line, from the cell's settings blob:
#   "settings": { "launch_args": ["-a", "untrusted", "-s", "read-only"] }
# The tool-gate cell needs codex launched with an approval policy that makes
# a model-proposed write escalate to the blocking approval overlay; other
# cells must NOT get those flags (read-only would break every cell that
# writes). Per-cell rather than a driver-wide constant for exactly that
# reason.
CODEX_LAUNCH_ARGS=()
if [[ -f "$SETTINGS_PATH" ]]; then
  while IFS= read -r _arg; do
    [[ -n "$_arg" ]] && CODEX_LAUNCH_ARGS+=("$_arg")
  done < <(jq -r '(.launch_args // [])[]' "$SETTINGS_PATH" 2>/dev/null || true)
fi
if [[ ${#CODEX_LAUNCH_ARGS[@]} -gt 0 ]]; then
  echo "[driver] launch_args: ${CODEX_LAUNCH_ARGS[*]}" >&2
fi

# Per-run CWD so codex creates sessions under a fresh path, isolating the
# trust dialog to this run. run-cell.sh's cross-adapter mode overrides
# this via $IRRLICHT_ONBOARD_CWD so a second, different adapter can share
# the SAME workspace (multiple-agents-same-workspace).
RUN_CWD="${IRRLICHT_ONBOARD_CWD:-$STAGING/cwd}"
mkdir -p "$RUN_CWD"

DEADLINE=$(( $(date +%s) + TIMEOUT_S ))
EXIT_REASON="ok"
# Raised to 1 by the epilogue, immediately before the final exit. cleanup() reads
# it to tell a run that FINISHED (EXIT_REASON is its verdict) apart from a `set
# -e` abort that never formed one (#1825) — see cleanup().
REACHED_EPILOGUE=0

# Active-session view — the step functions read/write these. They are a
# cache of the active slot's state, kept in sync via save_active /
# load_slot. TRANSCRIPT is the absolute rollout-*.jsonl path; UUID is the
# bare conversation UUID (.payload.id, the `codex resume <UUID>` arg);
# SESSION is the tmux session name; MARKER gates rollout discovery for
# this slot (resolve_transcript only considers rollouts NEWER than it).
SESSION=""
TRANSCRIPT=""
UUID=""
EXPECTED_TURNS=0
MARKER=""

# Per-slot state (1-based; index 0 unused). Each slot is one session
# lifetime. SES_OWNED[i]=1 while this driver still owns (has not retired) the
# slot's tmux session.
SES_SESSION=()
SES_TRANSCRIPT=()
# shellcheck disable=SC2034  # driver-owned slot array; the sourced replaydata/_lib/drive/slots.sh reads it (save_active/load_slot/alloc_slot)
SES_UUID=()
# shellcheck disable=SC2034  # driver-owned slot array; the sourced replaydata/_lib/drive/slots.sh reads it (save_active/load_slot/alloc_slot)
SES_EXPECTED=()
# shellcheck disable=SC2034  # driver-owned slot array; the sourced replaydata/_lib/drive/slots.sh reads it (save_active/load_slot/alloc_slot)
SES_MARKER=()
SES_CWD=()
SES_OWNED=()
N_SLOTS=0
ACTIVE=0

# Directories codex has already shown (and we accepted) the trust dialog
# for, this run. A second boot in any of these — a concurrent slot in the
# same cwd OR a resume relaunch of the same slot — won't re-prompt, so the
# trust-wait poll must be skipped or it stalls ~15s for a dialog that never
# appears.
TRUSTED_CWDS=()

# Slot bookkeeping (daemon_sid / save_active / load_slot / alloc_slot) is the
# shared model in lib/drive/slots.sh — byte-identical to pi's except the marker
# filename, set via DRIVE_MARKER_PREFIX (#508 #3).
_DRIVE_LIB="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../_lib/drive" && pwd)"
# shellcheck disable=SC2034  # read by the sourced replaydata/_lib/drive/slots.sh:56 (alloc_slot)
DRIVE_MARKER_PREFIX="$STAGING/.codex-start-marker"
# shellcheck source=../../_lib/drive/slots.sh
source "$_DRIVE_LIB/slots.sh"
# shellcheck source=../../_lib/drive/contracts.sh
source "$_DRIVE_LIB/contracts.sh"
# shellcheck source=../../_lib/drive/teardown.sh
source "$_DRIVE_LIB/teardown.sh"

# Always honor the staging contract: write driver.exit-reason on ANY exit
# (including a `set -e` abort mid-launch) and tear down EVERY tmux session this
# run allocated. Gated on session-name PRESENCE, never on SES_OWNED (#1825):
# nothing re-derives that array from `tmux has-session`, so a step that wrongly
# believed it killed a session would otherwise take the last net down with it.
# kill-session on an already-dead name (including the name a swap_after_slash
# slot shares with its successor) is a harmless no-op, which is exactly why
# presence is the right gate. Same shape as
# scripts/templates/drive-interactive.sh.tmpl:147-154.
#
# emit_session_contract (contracts.sh) ALSO writes driver.exit-reason on the
# normal path; this overwrites it with the same value. That double write is
# deliberate and idempotent — the trap is the one that covers the abort paths
# the epilogue never reaches.
# BEGIN cleanup
cleanup() {
  local i
  for (( i = 1; i <= N_SLOTS; i++ )); do
    [[ -n "${SES_SESSION[$i]:-}" ]] && tmux kill-session -t "${SES_SESSION[$i]}" 2>/dev/null || true
  done
  # An abort that never reached the epilogue never formed a verdict, so
  # EXIT_REASON is still its initial "ok". Writing that would report SUCCESS for
  # a failed run — the exact shape #1825 exists to stop — so record the
  # driver-fault reason instead. A verdict already formed (timeout, …) stands.
  if [[ "$REACHED_EPILOGUE" != "1" && "$EXIT_REASON" == "ok" ]]; then
    EXIT_REASON="nonzero(2)"
    echo "[driver] aborted before the epilogue — recording exit reason $EXIT_REASON, not ok" >&2
  fi
  echo "$EXIT_REASON" > "$STAGING/driver.exit-reason"
}
# END cleanup
trap cleanup EXIT

# The four boot gates codex can put on screen, as named predicates over a
# captured pane, with a committed corpus of real 0.147.0 captures behind them
# (#1388). Inline greps here could stop matching without failing anything —
# the gate then stays up, the prompt is typed into it, and the run records a
# healthy-looking fixture that fires zero hooks.
# shellcheck source=boot-gates.sh
source "$(dirname "${BASH_SOURCE[0]}")/boot-gates.sh"

# recipe-lint contract (#508 #4): the step types this driver genuinely ELICITS
# (a subset of its case arms — accepting a step type ≠ producing its effect),
# and whether slash commands need a dedicated step type. recipe-lint reads these
# constants directly, so the grammar has ONE owner — this driver — not a
# parallel manifest. Full tmux-TUI driver (claudecode set + fork).
# shellcheck disable=SC2034  # scraped from this file's SOURCE by tools/onboarding-factory/scripts/lib/recipe-lint.sh:97 (sed), never expanded in shell
DRIVE_ELICITS="send slash wait_turn interrupt keys sleep restart resume reset_session fork sigkill exit_clean start_session session"
# shellcheck disable=SC2034  # scraped from this file's SOURCE by tools/onboarding-factory/scripts/lib/recipe-lint.sh:113 (sed), never expanded in shell
DRIVE_SLASH_REQUIRES_STEP_TYPE=false

# Has the active slot's cwd already had its trust dialog accepted this
# run? Covers BOTH a concurrent second slot in the same cwd AND a resume
# relaunch of the same slot (codex won't re-prompt for a dir it already
# trusts), so the boot trust-wait can be skipped.
cwd_already_trusted() {
  local c="${SES_CWD[$ACTIVE]}" t
  for t in ${TRUSTED_CWDS[@]+"${TRUSTED_CWDS[@]}"}; do
    [[ "$t" == "$c" ]] && return 0
  done
  return 1
}

# boot_session brings up a codex TUI in the active slot's tmux session
# running the given argv, accepts the trust dialog (unless this slot's
# cwd was already trusted by an earlier slot this run), waits for the
# "OpenAI Codex" banner, and waits out the "Booting MCP" phase. Caller
# allocates the slot (alloc_slot) before invoking.
#
# Launch/boot notes:
#   --no-alt-screen keeps codex in inline mode so its output is
#   capturable via tmux pipe-pane (alt-screen would clear the screen on
#   every redraw and yield mostly noise).
#
#   Per-slot stdout: each slot pipes to $DRIVER_LOG.stdout.$ACTIVE. A
#   single shared file would interleave two concurrent panes' TUI
#   refreshes and confuse banner / trust detection.
#
#   Trust dialog: codex shows "Do you trust the contents of this
#   directory?" on first encounter with a directory. The pipe-pane LOG
#   splits that string across cursor-positioning escapes, so a literal
#   grep on the LOG misses it — poll the LIVE pane via capture-pane
#   instead, which renders the text contiguously.
#
#   Banner: "OpenAI Codex (vN.N.N)" renders contiguously in the LOG.
#   Generous cap (90s) because codex may auto-install npm updates on
#   launch.
#
#   Booting MCP: codex then spends ~5-15s booting MCP servers; keystrokes
#   typed during this phase have their Enter silently swallowed. Poll the
#   LIVE pane until "Booting MCP" is gone.
boot_session() {
  local sess="$SESSION" cwd="${SES_CWD[$ACTIVE]}"
  local slot_stdout="$DRIVER_LOG.stdout.$ACTIVE"
  : > "$slot_stdout"
  mkdir -p "$cwd"
  tmux kill-session -t "$sess" 2>/dev/null || true
  # Prefix with `env CODEX_HOME=…`: the pane's command is spawned by the tmux
  # SERVER, which may already have been running with a different (or absent)
  # CODEX_HOME long before this driver exported one — so inheriting it is not
  # something we can assume. Passing it on the command line pins the codex
  # process to the same home the daemon and CODEX_SESSIONS_DIR resolved.
  tmux new-session -d -s "$sess" -c "$cwd" env "CODEX_HOME=$CODEX_HOME_RESOLVED" "$@"
  tmux pipe-pane -t "$sess" -o "cat >> '$slot_stdout'"
  echo "[driver] tmux started: $sess (slot=$ACTIVE, cwd=$cwd, argv: $*)" >&2

  # Codex has FOUR independent startup gates. They look alike, they need
  # DIFFERENT answers, and conflating them is why codex had zero
  # hook-bearing recordings (#1388). The strings, the answers, the order they
  # render in and the measurements behind each are in boot-gates.sh, beside the
  # predicates the branches below call and the corpus that keeps them honest —
  # deliberately in ONE place, because a second copy of "what codex asks and
  # what to press" is what silently stops matching.
  #
  # The three properties that matter for reading this loop:
  #
  #   - DIRECTORY trust is answered "1", and the digit ALONE dismisses it.
  #     Cached per-cwd in config.toml as [projects."<cwd>"].trust_level.
  #   - HOOK trust comes in two shapes — a numbered MENU answered "2", and the
  #     review PANEL behind that menu's option 1, answered "t" then Escape.
  #     Cached per ENTRY as
  #     [hooks.state."<abs hooks.json>:<event>:<group>:<index>"].trusted_hash.
  #   - Every unanswered hook gate fails OPEN into "hooks won't run": the menu's
  #     third option is literally "3. Continue without trusting", and a panel
  #     that is never answered simply leaves every entry inactive. So a timed-out
  #     boot yields a completely normal-looking session that never fires a hook —
  #     a recording with zero hook_received and nothing to say why. That silent
  #     branch is the exact hole this cell is meant to close, and it is why the
  #     post-loop report below states hook-trust standing with evidence rather
  #     than inferring it from "no menu appeared".
  #
  # The hook gate keys on the hooks.json PATH + CONTENT, not on the cwd, so
  # cwd_already_trusted must not skip it: the recorder installs its own
  # resolved port into hooks.json (#1178), and a different port — or an
  # isolated CODEX_HOME — is new content under a new key, which re-prompts
  # even in a directory codex already trusts.
  # Every branch below is fire-once. `capture-pane` returns SCROLLBACK, not
  # just the live screen, so a menu's text keeps matching long after it has
  # been answered and scrolled away — an ungated branch therefore re-fires on
  # every poll, sends its keystroke again (the second Enter submitting the
  # digit as a user turn), starves the later branches in this if/elif chain,
  # and never lets `stable` climb, so the loop burns its full budget and the
  # hook menu falls through to "3. Continue without trusting". Measured with
  # the update branch ungated: it fired 40/40 iterations, the hook branch 0.
  local waited=0 dir_done=0 hooks_done=0 upd_done=0 stable=0 pane=""
  cwd_already_trusted && dir_done=1
  while [[ $waited -lt 60 ]]; do
    pane="$(tmux capture-pane -t "$sess" -p -S -40 2>/dev/null || true)"
    # Codex's self-update offer — "✨ Update available! ... 1. Update now
    # (runs `npm install -g @openai/codex`) / 2. Skip / 3. Skip until next
    # version" — renders BEFORE the banner and swallows the boot.
    #
    # This is not a cosmetic case. With no handler the menu simply sits
    # there: the trust poll and the banner wait both time out, and the first
    # step_send then types its prompt and presses Enter INTO THE MENU, whose
    # default is option 1. That silently runs a global `npm install -g` and
    # upgrades the operator's codex CLI mid-recording — observed doing
    # exactly that during #1388 (0.146.1 → 0.147.0), which also invalidates
    # the agent_cli_version the run is about to stamp into the fixture.
    # Answer "2" (Skip): a recording must never mutate the toolchain it is
    # measuring. Match on "Update now", the MENU's own line, rather than the
    # "Update available!" notice, which codex also prints non-interactively.
    if [[ $upd_done -eq 0 ]] && codex_pane_has_update_menu "$pane"; then
      tmux send-keys -t "$sess" "2"
      sleep 0.3
      tmux send-keys -t "$sess" Enter
      upd_done=1
      stable=0
      echo "[driver] declined codex self-update offer (2 = Skip)" >&2
      sleep 1
    elif [[ $hooks_done -eq 0 ]] && [[ "$(codex_hook_trust_answer "$pane")" == panel ]]; then
      # The hook REVIEW PANEL — the screen behind the menu's "1. Review hooks",
      # and where #1388's own run ended up when a stray Enter selected it. It is
      # answered with a bare `t` (trust all) and closed with Escape; it does NOT
      # accept the menu's "2", and it does not close itself.
      #
      # It sits AHEAD of the menu branch, and that order is load-bearing rather
      # than alphabetical. $pane is a 40-line SCROLLBACK read, so once a run has
      # reached the panel the menu's own text is still in it — the panel is only
      # ever reachable THROUGH the menu, never the reverse. Checking the menu
      # first would therefore send "2" at a screen where 2 is not a choice,
      # i.e. type a literal 2 into the panel. The live screen is what has focus,
      # and the panel is the later of the two.
      #
      # Confirmed off the LIVE SCREEN (no -S), not scrollback: the panel redraws
      # in place and `-S -N` keeps returning the pre-trust frame, so a scrollback
      # read cannot distinguish "still asking" from "already answered". The
      # config.toml trusted_hash is not usable here either — measured, codex had
      # written no [hooks] section at all 5s after the panel reported every entry
      # Active.
      local try=0
      while [[ $try -lt 5 ]]; do
        tmux send-keys -t "$sess" "t"
        sleep 1.2
        if codex_pane_hook_panel_is_trusted \
             "$(tmux capture-pane -t "$sess" -p 2>/dev/null || true)"; then
          hooks_done=1
          break
        fi
        try=$((try + 1))
      done
      tmux send-keys -t "$sess" Escape
      sleep 0.5
      stable=0
      if [[ $hooks_done -eq 1 ]]; then
        echo "[driver] accepted hook-trust panel (t = trust all)" >&2
      else
        echo "[driver] WARNING: hook-trust panel never reported every entry trusted" >&2
      fi
    elif [[ $hooks_done -eq 0 ]] && [[ "$(codex_hook_trust_answer "$pane")" == menu ]]; then
      # Confirm the menu actually CLOSED before believing it. codex swallows
      # keystrokes during the boot/MCP phase (see step_send's render delay),
      # so a send that silently dropped would otherwise set hooks_done=1, hide
      # the still-open menu from this loop, and let the run continue with
      # hooks disabled — the healthy-looking zero-hook recording #1388 is about.
      local try=0
      while [[ $try -lt 5 ]]; do
        tmux send-keys -t "$sess" "2"
        sleep 0.3
        tmux send-keys -t "$sess" Enter
        sleep 1.2
        if ! codex_pane_has_hook_menu \
               "$(tmux capture-pane -t "$sess" -p 2>/dev/null || true)"; then
          hooks_done=1
          break
        fi
        try=$((try + 1))
      done
      stable=0
      if [[ $hooks_done -eq 1 ]]; then
        echo "[driver] accepted hook-trust menu (2 = Trust all and continue)" >&2
      else
        echo "[driver] WARNING: hook-trust menu still on screen after 5 attempts" >&2
      fi
    elif [[ $dir_done -eq 0 ]] && codex_pane_has_dir_trust "$pane"; then
      # The digit ALONE dismisses this dialog on 0.147.0 — measured by a probe
      # that sent only "1" and watched the hook menu replace it with no further
      # keystroke for 3.6s. An unconditional Enter after it therefore lands on
      # whatever renders NEXT, and what renders next is the hook-trust menu whose
      # preselected row is "1. Review hooks": that is precisely how #1388's own
      # recording run walked past the hook gate into the review panel and
      # recorded zero hook_received while reporting exit-reason "ok".
      #
      # So the Enter is sent only if the dialog is still up, off the LIVE screen.
      # An older codex that needs the confirm keeps working; a codex that does
      # not never gets a keystroke it did not ask for.
      tmux send-keys -t "$sess" "1"
      sleep 0.6
      if codex_pane_has_dir_trust \
           "$(tmux capture-pane -t "$sess" -p 2>/dev/null || true)"; then
        tmux send-keys -t "$sess" Enter
        echo "[driver] accepted directory trust dialog (1, then Enter to confirm)" >&2
      else
        echo "[driver] accepted directory trust dialog (1 = yes; no Enter needed)" >&2
      fi
      dir_done=1
      stable=0
      sleep 1
    elif grep -aq 'OpenAI Codex' "$slot_stdout" 2>/dev/null; then
      # Banner is up and no gate is on screen. Require a few consecutive
      # clear polls before believing it: the hook menu can render AFTER the
      # banner (it is gated on config load, not on the splash), so breaking
      # on the first clear poll would walk past it and silently disable
      # hooks for the whole run.
      stable=$((stable + 1))
      [[ $stable -ge 8 ]] && break
    fi
    sleep 0.5
    waited=$((waited + 1))
  done
  # Report hook-trust standing with EVIDENCE, never as a bare reassurance.
  # "no menu appeared" has two very different causes — already trusted, or
  # the menu was missed — and only one of them records hooks. codex writes a
  # trusted_hash per entry under [hooks.state]."<abs hooks.json>:<event>:…",
  # so the config is the thing that actually knows.
  if [[ $hooks_done -eq 1 ]]; then
    echo "[driver] hook trust: granted this boot" >&2
  elif [[ ! -f "$CODEX_HOME_RESOLVED/hooks.json" ]]; then
    echo "[driver] hook trust: n/a — no $CODEX_HOME_RESOLVED/hooks.json installed" >&2
  elif grep -qF -e "hooks.state.\"$CODEX_HOME_PHYS/hooks.json:" \
                -e "hooks.state.\"$CODEX_HOME_RESOLVED/hooks.json:" \
         "$CODEX_HOME_RESOLVED/config.toml" 2>/dev/null; then
    echo "[driver] hook trust: already trusted (trusted_hash present in config.toml)" >&2
  else
    echo "[driver] WARNING: hooks.json is installed but NOT trusted and no menu was" >&2
    echo "[driver] WARNING: answered — codex will not fire hooks this run (#1388)." >&2
    tmux capture-pane -t "$sess" -p -S -15 2>/dev/null | sed 's/^/[driver]   | /' >&2
  fi
  # Remember this cwd so a later resume/concurrent boot here skips the
  # DIRECTORY dialog. Hook trust is deliberately not cached here — it is
  # keyed on hooks.json, not on the cwd.
  TRUSTED_CWDS+=("$cwd")

  local waited=0 banner_seen=0
  while [[ $waited -lt 180 ]]; do
    if [[ -f "$slot_stdout" ]] && grep -aq 'OpenAI Codex' "$slot_stdout" 2>/dev/null; then
      banner_seen=1
      break
    fi
    sleep 0.5
    waited=$((waited + 1))
  done
  if [[ $banner_seen -eq 0 ]]; then
    # Say so LOUDLY rather than falling through. Everything after this types
    # text and presses Enter; if the banner never came, something else owns
    # the screen — an unrecognized modal — and those keystrokes land in it.
    # That is how the self-update offer above got accepted before it had a
    # handler, so an unexplained boot deserves a line in the log naming the
    # pane's actual contents rather than a silent continue.
    echo "[driver] WARNING: codex banner never appeared after 90s on slot $ACTIVE." >&2
    echo "[driver] WARNING: an unhandled prompt may be holding the pane; last 15 lines:" >&2
    tmux capture-pane -t "$sess" -p -S -15 2>/dev/null | sed 's/^/[driver]   | /' >&2
  fi

  waited=0
  while [[ $waited -lt 60 ]]; do
    if ! tmux capture-pane -t "$sess" -p -S -20 2>/dev/null | grep -q 'Booting MCP'; then
      break
    fi
    sleep 0.5
    waited=$((waited + 1))
  done
  sleep 2  # extra grace for the input prompt to settle
}

# transcript_claimed reports whether an absolute rollout path is already
# bound to a DIFFERENT slot, so concurrent discovery never double-binds
# the same rollout when per-slot markers collide at 1s mtime granularity.
transcript_claimed() {
  local p="$1" i
  for (( i = 1; i <= N_SLOTS; i++ )); do
    [[ $i -eq $ACTIVE ]] && continue
    [[ "${SES_TRANSCRIPT[$i]}" == "$p" ]] && return 0
  done
  return 1
}

# Codex creates its rollout file under CODEX_SESSIONS_DIR only after the
# first user message is processed — there's nothing to read at boot.
# Discovery finds the newest rollout-*.jsonl NEWER than this slot's
# $MARKER that isn't already bound to another slot; after a /new or
# /fork (which bump the marker) the prior rollout is excluded so the new
# one is picked up. With concurrent sessions each slot resolves on its
# first wait_turn — before the next session is booted — and caches the
# result, so later focus switches reuse the bound path.
resolve_transcript() {
  if [[ -n "$TRANSCRIPT" ]]; then return 0; fi
  for _ in $(seq 1 60); do
    local candidate=""
    local f
    while IFS= read -r f; do
      [[ -z "$f" ]] && continue
      transcript_claimed "$f" && continue
      candidate="$f"
    done < <(find "$CODEX_SESSIONS_DIR" -maxdepth 5 -type f \
                  -name 'rollout-*.jsonl' -newer "$MARKER" 2>/dev/null | sort)
    if [[ -n "$candidate" && -s "$candidate" ]]; then
      TRANSCRIPT="$candidate"
      UUID="$(head -n1 "$TRANSCRIPT" | jq -r '.payload.id // empty' 2>/dev/null || true)"
      [[ -n "$UUID" ]] || { TRANSCRIPT=""; sleep 0.5; continue; }
      echo "[driver] resolve_transcript[s$ACTIVE]: $TRANSCRIPT (uuid=$UUID, sid=$(daemon_sid "$TRANSCRIPT"))" >&2
      return 0
    fi
    sleep 0.5
  done
  return 1
}

# Count completed turns by jq-counting the canonical codex turn-done
# shape: {"type":"event_msg","payload":{"type":"task_complete",...}}.
# Mirrors core/adapters/inbound/agents/codex/parser.go which classifies
# task_complete as turn_done. Other event_msg types (task_started,
# turn_aborted, agent_message, token_count) are intentionally excluded —
# in particular, an Escape-interrupted turn produces turn_aborted (not
# task_complete) and must NOT be counted as a completed turn.
# turn_count lives in a sibling lib so it can be unit-tested without executing
# this driver (#1333 / B4). See replaydata/_lib/drive/turn-count_test.sh.
# shellcheck source=turn-count.sh
source "$(dirname "${BASH_SOURCE[0]}")/turn-count.sh"

step_send() {
  local text="$1"
  tmux send-keys -t "$SESSION" -l -- "$text"
  # Brief pause so codex's Ink-based input handler renders the typed
  # text before Enter lands. Without this, Enter races the render and
  # is silently dropped — the text stays in the input box, no
  # task_started fires, no rollout file is created.
  sleep 0.3
  tmux send-keys -t "$SESSION" Enter
  EXPECTED_TURNS=$((EXPECTED_TURNS + 1))
  echo "[driver] send[s$ACTIVE]: ${text:0:60} (expecting turn $EXPECTED_TURNS)" >&2
}

step_wait_turn() {
  resolve_transcript || {
    echo "[driver] wait_turn[s$ACTIVE]: codex never created a rollout under $CODEX_SESSIONS_DIR" >&2
    EXIT_REASON="readiness_timeout"
    return 1
  }
  local now=0
  while [[ $(date +%s) -lt $DEADLINE ]]; do
    now=$(turn_count)
    if [[ $now -ge $EXPECTED_TURNS ]]; then
      echo "[driver] wait_turn[s$ACTIVE]: count=$now (expected ≥ $EXPECTED_TURNS)" >&2
      return 0
    fi
    sleep 1
  done
  echo "[driver] wait_turn[s$ACTIVE]: timeout (count=$now, expected ≥ $EXPECTED_TURNS)" >&2
  EXIT_REASON="timeout"
  return 1
}

step_interrupt() {
  # Codex's TUI binds Escape to "cancel the in-flight LLM turn" (its own
  # status footer says "esc to interrupt" while a turn is running). The
  # cancelled turn lands as event_msg/turn_aborted with no task_complete,
  # so the task_complete-only turn-counter naturally skips it.
  tmux send-keys -t "$SESSION" Escape
  if [[ $EXPECTED_TURNS -gt 0 ]]; then
    EXPECTED_TURNS=$((EXPECTED_TURNS - 1))
  fi
  echo "[driver] interrupt[s$ACTIVE] (Escape, expecting turn $EXPECTED_TURNS)" >&2
  sleep 1
}

# swap_after_slash <slash-text> — shared handler for /new (reset_session)
# and /fork (fork). Both abandon the current rollout and cause codex to
# write subsequent turns to a NEW rollout with a fresh session_id, in the
# SAME process:
#   /new   is LAZY  — the new rollout materializes only on the first
#                     post-reset user message.
#   /fork  is EAGER — the new rollout materializes the instant the
#                     command runs (carrying replayed pre-fork history).
# Either way: resolve the current rollout (so its session_id is recorded
# in the slot list), send the slash, then allocate a NEW slot that reuses
# the same tmux/process. The new slot's fresh marker makes the next
# wait_turn's resolve_transcript find the new rollout.
swap_after_slash() {
  local slash="$1"
  resolve_transcript || true
  local old_tmux="$SESSION"
  local old_cwd="${SES_CWD[$ACTIVE]}"
  save_active
  # The old rollout is frozen; retire the slot but keep it in the list so
  # the epilogue flushes its session_id. The process keeps running (the
  # new slot reuses its tmux), so it is killed exactly once at teardown.
  SES_OWNED[$ACTIVE]=0
  echo "[driver] swap ($slash): recorded old session sid=$(daemon_sid "$TRANSCRIPT")" >&2

  tmux send-keys -t "$old_tmux" -l -- "$slash"
  sleep 0.3
  tmux send-keys -t "$old_tmux" Enter

  # Allocate the new slot reusing the same tmux/process. alloc_slot mints
  # a fresh marker; sleep first so it sorts strictly after the old
  # rollout's mtime (1s granularity), then re-touch to be safe.
  sleep 1
  alloc_slot "$old_tmux" "$old_cwd"
  SES_OWNED[$ACTIVE]=1
  touch "$MARKER"
  echo "[driver] swap ($slash): new slot #${ACTIVE}, marker bumped, awaiting new rollout" >&2
  sleep 1
}

step_exit_clean() {
  # codex's TUI binds Ctrl-D to "exit". Ctrl-D triggers a graceful
  # shutdown so codex flushes its rollout and the daemon emits
  # process_exited.
  tmux send-keys -t "$SESSION" C-d
  # STRICT poll (#1825): the old best-effort wait_tmux_session_gone returns 0
  # even when the cap expires with the session still up, and SES_OWNED=0 was set
  # regardless — so an exit key that stopped working (as claude's did) read
  # exactly like one that worked, and the run still reported exit-reason=ok.
  # Cap: DRIVE_EXIT_CLEAN_CAP_S (_lib/drive/teardown.sh). This site passed 2s
  # until the #1825 review. That 2 was the pre-#1018 SETTLE budget, where
  # overrunning was free; the strict poll turned the same number into a hard
  # deadline that SIGHUPs the pane mid-flush and fails the run. It is now the
  # fleet-uniform generous bound, and that constant carries how the number was
  # arrived at: it is a bound, not a measurement.
  if require_tmux_session_gone "$SESSION" "$DRIVE_EXIT_CLEAN_CAP_S"; then
    SES_OWNED[$ACTIVE]=0
    echo "[driver] exit_clean: sent Ctrl-D to $SESSION (session gone)" >&2
  else
    echo "[driver] exit_clean: FAILED — $SESSION still alive ${DRIVE_EXIT_CLEAN_CAP_S}s after Ctrl-D;" \
         "killing it explicitly. codex did NOT shut down gracefully, so this" \
         "recording has no real clean-exit process_exited." >&2
    tmux kill-session -t "$SESSION" 2>/dev/null || true
    SES_OWNED[$ACTIVE]=0
    EXIT_REASON="nonzero(2)"
  fi
}

step_resume() {
  # Resume the active codex conversation in a new process lifetime.
  # Exit the running codex cleanly (Ctrl-D), kill its tmux session, then
  # relaunch `codex resume <UUID> --no-alt-screen`. Codex APPENDS to the
  # SAME rollout file (same session_id) across both lifetimes — verified
  # empirically — so this is ONE session (one slot) with two process
  # lifetimes: TRANSCRIPT/UUID/MARKER stay unchanged and we do NOT
  # allocate a new slot (which would double-list the rollout and
  # double-concat the transcript at curate time). Only the tmux session
  # name rotates.
  resolve_transcript || true
  local resume_uuid="$UUID"
  local saved_transcript="$TRANSCRIPT"

  tmux send-keys -t "$SESSION" C-d
  wait_tmux_session_gone "$SESSION" 2
  tmux kill-session -t "$SESSION" 2>/dev/null || true
  sleep 1

  SESSION="codex-onboard-$(date +%s)-$$-r${ACTIVE}"
  SES_SESSION[$ACTIVE]="$SESSION"

  # Keep the SAME rollout cached across the relaunch: codex appends to it
  # rather than minting a new one, so resolve_transcript must NOT run
  # again (clearing+re-finding risks racing the new process before it
  # reopens the rollout). Keep TRANSCRIPT/UUID/EXPECTED_TURNS as-is.
  if [[ -n "$resume_uuid" ]]; then
    echo "[driver] resume[s$ACTIVE]: relaunch codex resume $resume_uuid (same rollout=$saved_transcript)" >&2
    boot_session codex resume "$resume_uuid" ${CODEX_LAUNCH_ARGS[@]+"${CODEX_LAUNCH_ARGS[@]}"} --no-alt-screen
  else
    echo "[driver] resume[s$ACTIVE]: UUID unknown — relaunch codex resume --last" >&2
    boot_session codex resume --last ${CODEX_LAUNCH_ARGS[@]+"${CODEX_LAUNCH_ARGS[@]}"} --no-alt-screen
  fi
}

step_sigkill() {
  # kill -9 the active slot's codex process — abrupt teardown with no
  # rollout flush (the SIGKILL counterpart to exit_clean's graceful
  # Ctrl-D). Unlike drive-claudecode-interactive.sh, which pgreps
  # `--session-id <uuid>` out of argv, codex's argv is uniform (every
  # session is just `codex --no-alt-screen`), so there is no per-session
  # argv marker to match. Instead target exactly the process the daemon
  # tracks: codex holds its rollout .jsonl open for writing for the whole
  # session lifetime, and the daemon discovers the PID as that write-FD
  # holder (codex/pid.go → DiscoverPIDByTranscriptWriter, via lsof).
  # Mirroring that lookup here guarantees the SIGKILL lands on the daemon's
  # PID, so process_exited fires.
  resolve_transcript || true
  local pid=""
  if [[ -n "$TRANSCRIPT" ]]; then
    # Same lsof write-FD match as the daemon: COMMAND PID USER FD …. Accept
    # 'w' (write-only) AND 'u' (read/write) — codex 0.147 holds its rollout
    # as "59u", so a /w$/ match finds nothing, and a locked handle ("59uW")
    # ends in the lock character rather than the mode. This mirrors
    # processlifecycle.WriterOf, which had the same bug (#1388); keep the two
    # in step, or this falls back to the pane pid and can SIGKILL the wrong
    # process.
    pid=$(lsof "$TRANSCRIPT" 2>/dev/null | awk 'NR>1 && $4 ~ /^[0-9]+[wu]/ {print $2; exit}')
  fi
  # Fallback: the codex process in this slot's tmux pane. Resolve the codex
  # descendant of the pane (in case tmux wrapped the command in a shell) so
  # the SIGKILL can't merely orphan codex — an orphaned codex keeps writing
  # and the daemon would never observe process_exited.
  if [[ -z "$pid" ]]; then
    local pane_pid
    pane_pid=$(tmux list-panes -t "$SESSION" -F '#{pane_pid}' 2>/dev/null | head -1)
    if [[ -n "$pane_pid" ]]; then
      pid=$(pgrep -x codex -P "$pane_pid" 2>/dev/null | head -1)
      [[ -z "$pid" ]] && pid="$pane_pid"
    fi
  fi
  # Leave the dead tmux pane for teardown — the kill alone produces
  # process_exited.
  if [[ -n "$pid" ]]; then
    echo "[driver] sigkill[s$ACTIVE]: killed PID $pid (sid=$(daemon_sid "$TRANSCRIPT"))" >&2
  else
    echo "[driver] sigkill[s$ACTIVE]: no codex PID found (transcript=${TRANSCRIPT:-none}, session=$SESSION)" >&2
  fi
  sigkill_and_wait "$pid" 1
  SES_OWNED[$ACTIVE]=0
}

step_restart() {
  # End the active session and start a FRESH codex (new rollout, new
  # session_id, fresh cwd). Mirrors drive-claudecode-interactive.sh's
  # restart: used between session-end variants so each lands as its own
  # session row, separated by a grey gap where no session is alive. By the
  # time restart runs the active process is usually already gone (an
  # exit_clean or sigkill preceded it); retire the slot regardless but keep
  # it in the list so the epilogue flushes its session_id. A fresh cwd
  # keeps each variant's rollout cleanly separated and gives it its own
  # trust state (codex caches trust per directory).
  resolve_transcript || true
  save_active
  # shellcheck disable=SC2034  # SES_OWNED is write-only here since #1825 — teardown now gates on session-name PRESENCE instead of this flag, which is exactly what leaked a live agent + tmux session per exit_clean run. kiro-cli:567 and antigravity:537 legitimately branch on their own copies (a retired-slot guard).
  SES_OWNED[$ACTIVE]=0
  tmux kill-session -t "$SESSION" 2>/dev/null || true
  sleep 1
  local idx=$(( N_SLOTS + 1 ))
  alloc_slot "codex-onboard-$(date +%s)-$$-${idx}" "${RUN_CWD}-${idx}"
  echo "[driver] restart: new session slot #${ACTIVE} (cwd=${RUN_CWD}-${idx})" >&2
  boot_session codex ${CODEX_LAUNCH_ARGS[@]+"${CODEX_LAUNCH_ARGS[@]}"} --no-alt-screen
}

step_start_session() {
  # Launch a NEW concurrent codex session WITHOUT tearing down the active
  # one. The previous session keeps running (its tmux survives), so the
  # daemon observes both as independent session rows. Defaults to session
  # 1's cwd (the same-cwd scenario); pass a directory to launch elsewhere.
  local req_cwd="$1"
  # Claim the current slot's rollout BEFORE spawning a concurrent one. If
  # the prior slot hasn't been resolved (e.g. start_session issued before
  # its wait_turn) its rollout is unclaimed, and a turn still streaming
  # there keeps advancing its mtime past the new slot's just-touched
  # marker — the new slot's resolve_transcript could then bind to the OLD
  # rollout. Resolving here marks it claimed so transcript_claimed excludes
  # it from the new slot's discovery.
  resolve_transcript || true
  save_active
  local idx=$(( N_SLOTS + 1 ))
  local new_cwd="${req_cwd:-$RUN_CWD}"
  alloc_slot "codex-onboard-$(date +%s)-$$-${idx}" "$new_cwd"
  echo "[driver] start_session: concurrent session slot #${ACTIVE} (cwd=$new_cwd)" >&2
  boot_session codex ${CODEX_LAUNCH_ARGS[@]+"${CODEX_LAUNCH_ARGS[@]}"} --no-alt-screen
}

# Bring up the first session as slot 1. SCRIPT_JSON's reset_session/fork/
# start_session steps allocate further slots; resume relaunches in place.
alloc_slot "codex-onboard-$(date +%s)-$$" "$RUN_CWD"
boot_session codex ${CODEX_LAUNCH_ARGS[@]+"${CODEX_LAUNCH_ARGS[@]}"} --no-alt-screen

# Iterate steps. EXIT_REASON / array updates persist via the parent shell
# (process substitution feeds the loop — the body is NOT subshelled).
STEP_OK=true
while read -r step; do
  if ! $STEP_OK; then break; fi
  type=$(jq -r '.type' <<<"$step")

  # Optional inline session target: switch the active context to slot N
  # before executing the step. start_session is exempt (it allocates its
  # own slot). A target slot must already exist.
  tgt=$(jq -r '.session // empty' <<<"$step")
  if [[ -n "$tgt" && "$type" != "start_session" && "$tgt" != "$ACTIVE" ]]; then
    if [[ "$tgt" =~ ^[0-9]+$ && "$tgt" -ge 1 && "$tgt" -le "$N_SLOTS" ]]; then
      save_active
      load_slot "$tgt"
      echo "[driver] switch -> session slot $tgt (uuid=$UUID)" >&2
    else
      echo "[driver] switch: invalid session slot '$tgt' (have $N_SLOTS)" >&2
      EXIT_REASON="nonzero(2)"
      STEP_OK=false
      continue
    fi
  fi

  case "$type" in
    send|slash)
      step_send "$(jq -r '.text' <<<"$step")"
      ;;
    wait_turn)
      step_wait_turn || STEP_OK=false
      ;;
    interrupt)
      step_interrupt
      ;;
    keys)
      # Raw tmux key sequence (NOT literal text) for navigating picker UIs
      # such as codex's /model two-step selector. Example:
      #   {"type":"keys","keys":"Down Down Enter"}
      ks=$(jq -r '.keys' <<<"$step")
      # shellcheck disable=SC2086  # intentional word-splitting of the key list
      tmux send-keys -t "$SESSION" $ks
      echo "[driver] keys[s$ACTIVE]: $ks" >&2
      sleep 0.5
      ;;
    sleep)
      secs=$(jq -r '.seconds // 1' <<<"$step")
      echo "[driver] sleep: ${secs}s" >&2
      sleep "$secs"
      ;;
    reset_session)
      swap_after_slash "/new"
      ;;
    fork)
      swap_after_slash "/fork"
      ;;
    exit_clean)
      step_exit_clean
      ;;
    sigkill)
      step_sigkill
      ;;
    restart)
      step_restart
      ;;
    resume)
      step_resume
      ;;
    start_session)
      step_start_session "$(jq -r '.cwd // empty' <<<"$step")"
      ;;
    session)
      # Pure focus switch — already handled by the inline target block.
      :
      ;;
    *)
      echo "[driver] unknown step type: $type" >&2
      EXIT_REASON="nonzero(2)"
      STEP_OK=false
      ;;
  esac
done < <(jq -c '.[]' <<<"$SCRIPT_JSON")

# Persist the final active state.
save_active

# Best-effort: any slot that never resolved a transcript (e.g. a script
# with no wait_turn for that session) gets one last resolution attempt.
for (( i = 1; i <= N_SLOTS; i++ )); do
  if [[ -z "${SES_TRANSCRIPT[$i]}" ]]; then
    load_slot "$i"
    resolve_transcript || true
    save_active
  fi
done

sleep 0.5

# Tear down every session this run allocated — gated on session-name PRESENCE,
# not on SES_OWNED (#1825). SES_OWNED records what the driver believes it still
# owns; a step that cleared ownership for a session still running (claudecode's
# exit_clean did exactly that) made this loop skip the one kill that would have
# caught the leak.
for (( i = 1; i <= N_SLOTS; i++ )); do
  if [[ -n "${SES_SESSION[$i]:-}" ]]; then
    tmux kill-session -t "${SES_SESSION[$i]}" 2>/dev/null || true
  fi
done

{
  echo "=== stdout ==="
  for (( i = 1; i <= N_SLOTS; i++ )); do
    if [[ -f "$DRIVER_LOG.stdout.$i" ]]; then
      echo "--- session slot $i (sid=$(daemon_sid "${SES_TRANSCRIPT[$i]}")) ---"
      cat "$DRIVER_LOG.stdout.$i" 2>/dev/null || true
      echo
    fi
  done
  echo
  echo "=== exit reason: $EXIT_REASON ==="
} > "$DRIVER_LOG"
# Staging contract: primary session.uuid is codex's daemon-side session_id
# (rollout filename stem) so run-cell's primary-skip comparison and curate's
# `.session_id` filter both match. emit_session_contract handles the combined
# stdout, exit-reason, and the multi-session lists (lib/drive/contracts.sh).
emit_session_contract "$(daemon_sid "${SES_TRANSCRIPT[1]}")"

echo "drive-codex-interactive: $EXIT_REASON (slots=${N_SLOTS}, primary=$(daemon_sid "${SES_TRANSCRIPT[1]}"), transcript=${SES_TRANSCRIPT[1]})"

# The epilogue completed: EXIT_REASON is this run's real verdict, so cleanup()
# must record it as-is rather than rewrite it as an abort.
REACHED_EPILOGUE=1
drive_exit
