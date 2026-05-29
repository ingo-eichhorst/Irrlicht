#!/usr/bin/env bash
# drive-opencode-interactive.sh — drive opencode via headless `opencode run`
# subprocess invocations, executing a step-script (send / wait_turn / sleep).
#
# OpenCode has a true headless mode (`opencode run`) — each `send` step
# launches an `opencode run` subprocess and waits for it to complete.
# `wait_turn` becomes a no-op because `opencode run` already blocks until
# the turn ends. This is structurally simpler than the claudecode tmux+TUI
# driver and matches how opencode is most often automated.
#
# Per-turn model selection (model-select): a `send` step may carry an
# optional `model` field; when present the headless arm passes
# `opencode run -m <provider/model>` so that turn runs on the named model.
# Omitting it runs on the config default (unchanged for every other cell).
# This is how a single --session chain runs turn 1 on model A and turn 2 on
# model B — a real mid-session switch the daemon observes per turn (it reads
# message.data.model.modelID from the SQLite store, latest-wins). It is a
# headless-path enhancement, NOT a TUI/run_live change — the in-REPL /models
# picker is an arrow-key overlay and stays out of scope.
#
# Session continuity: the first `send` launches a fresh session in
# <staging>/cwd; subsequent `send` steps use `--session <id>` with the
# captured id so the conversation chains within one session record.
#
# Concurrency (multiple headless sessions in one cwd):
#   start_session — begin a SECOND independent `opencode run` chain in the
#                   SAME RUN_CWD without disturbing the first. opencode keys
#                   sessions on the SQLite session.id (the directory column
#                   is only a liveness gate — watcher.go scanSessions), so a
#                   second chain in one cwd is a second, independent
#                   observable arc (daemon_capability: full). This is the
#                   headless analogue of claudecode's start_session: it
#                   allocates a new "slot" with an empty session id, so the
#                   next `send` (no --session) mints a fresh ses_ row; that
#                   slot then chains --session <ses2> for its later sends.
#                   The first slot keeps chaining its own ses1.
#   any step may carry {"session": N} to switch the active slot to N
#   (1-based) before executing — e.g. route a later `send` back to slot 1
#   while slot 2 stays alive. A bare {"type":"session","session":N} just
#   switches focus. This is the headless port of the claudecode driver's
#   inline {"session":N} target + start_session/session step types; it
#   stays on the deterministic headless path (no tmux/TUI), so the two
#   arcs are two interleaved `opencode run --session <ses>` chains.
#
# Contract files written to <staging-dir>:
#   driver.log[.stdout|.stderr]  — captured CLI output
#   driver.exit-reason           — ok|timeout|killed|nonzero(N)
#   transcript.path              — slot-1 exported parts JSONL (back-compat)
#   session.uuid                 — slot-1 opencode session id (ses_…)
#   session.uuids                — every slot's ses_ id, one per line
#   transcript.paths             — every slot's exported parts JSONL path
#     (single-slot runs write one line each — same shape, so run-cell.sh's
#      multi-session curation is a no-op below count 2; a start_session run
#      writes BOTH ses_ ids so curate-lifecycle-fixture.sh captures both
#      arcs, exactly like codex's reset_session contract.)
#
# Usage:
#   drive-opencode-interactive.sh <staging-dir> <preferred-uuid> \
#       <timeout-seconds> <settings-path> <script-json>
#
# The preferred-uuid is ignored — opencode auto-assigns session ids and
# does not accept a caller-chosen one.

set -euo pipefail

if [[ $# -ne 5 ]]; then
  echo "usage: drive-opencode-interactive.sh <staging> <uuid-ignored> <timeout-s> <settings-path> <script-json>" >&2
  exit 2
fi

STAGING="$1"
# UUID ($2) ignored — opencode mints its own session id.
TIMEOUT_S="$3"
# SETTINGS_PATH ($4): a recipe `settings` blob. opencode resolves config
# from a project-local opencode.json in the run directory (then walks up to
# the global ~/.config/opencode/opencode.json), so a non-empty blob is
# seeded as RUN_CWD/opencode.json below to drive per-run policy — most
# importantly the `permission` classifier (bash/edit/… = allow|ask|deny,
# bash wildcards). An empty blob ({} / absent) writes NO file, preserving
# the prior global-config-only behavior for every other opencode cell.
SETTINGS_PATH="$4"
SCRIPT_JSON="$5"

mkdir -p "$STAGING"
DRIVER_LOG="$STAGING/driver.log"

# recipe-lint contract (#508 #4): the step types this driver genuinely ELICITS
# and whether slash needs a dedicated step type. recipe-lint reads these
# directly so the grammar has ONE owner here, not a parallel manifest. Hybrid
# driver: headless `opencode run` (send/wait_turn/sleep/start_session/session)
# IGNORES in-REPL slash commands — it stores a leading-slash text as a literal
# user message — so slash_requires_step_type is true. The live-TUI path
# (run_live) genuinely elicits slash/reset_session/interrupt/keys/restart/
# sigkill/mid_turn_send, but ONLY via those STEP TYPES (a bare send "/cmd" never
# reaches the REPL). The `live` no-op marker forces a plain send/wait_turn
# recipe onto the long-lived run_live path.
DRIVE_ELICITS="send wait_turn sleep slash reset_session interrupt keys restart sigkill mid_turn_send start_session session live"
DRIVE_SLASH_REQUIRES_STEP_TYPE=true

# Per-run cwd so each scenario launches a fresh opencode project context.
# OpenCode keys sessions on the directory column in the SQLite session
# table; isolating cwd guarantees the session-lookup query at the end
# finds OUR session even if the user has other recent opencode runs.
# A CROSS-ADAPTER cell (multiple-agents-same-workspace) forces a SHARED
# workspace via $IRRLICHT_ONBOARD_CWD so a different adapter coexists in
# the same cwd — the daemon then keys both sessions to the same cwd slug.
# The session-lookup query still finds OUR session: opencode.db's session
# table only ever holds opencode sessions, so directory = $RUN_CWD +
# ORDER BY time_created DESC picks our row regardless of the partner agents
# (which write to their own stores, never opencode.db).
RUN_CWD="${IRRLICHT_ONBOARD_CWD:-$STAGING/cwd}"
mkdir -p "$RUN_CWD"
# Canonicalize (resolve symlinks) so the session-lookup WHERE clause matches
# what opencode stores: node's process.cwd() writes the RESOLVED path into
# session.directory, so on macOS a /tmp/... cwd is stored as /private/tmp/...
# — querying the unresolved value would find no row and silently mis-record.
RUN_CWD="$(cd "$RUN_CWD" && pwd -P)"
# SQL-literal-safe form: double every single quote so a cwd path containing an
# apostrophe can't terminate the quoted string literal in the WHERE clauses
# below (which would error the query → the `|| echo 0` mask would silently
# zero the turn count → every wait_turn would time out). Use $RUN_CWD_SQL
# inside SQL string literals; $RUN_CWD everywhere else (cd, mkdir, lsof match).
RUN_CWD_SQL="${RUN_CWD//\'/\'\'}"

# Seed a project-local opencode.json from the recipe's `settings` blob when
# it carries config (a non-empty object). opencode loads project config from
# the run directory before the global ~/.config/opencode/opencode.json, so
# this is how a cell pins a per-run `permission` policy (the auto-classifier:
# bash/edit/… = allow|ask|deny + bash wildcards) without touching the user's
# global config. The $schema is injected if the blob omits it so opencode
# validates it as a real config. An empty/absent blob writes NOTHING, so
# every other opencode cell keeps using the global-config-only path.
if [[ -n "${SETTINGS_PATH:-}" && -f "$SETTINGS_PATH" ]]; then
  if [[ "$(jq -r 'if (type=="object" and (.|length)>0) then "yes" else "no" end' "$SETTINGS_PATH" 2>/dev/null)" == "yes" ]]; then
    jq '. + (if has("$schema") then {} else {"$schema":"https://opencode.ai/config.json"} end)' \
      "$SETTINGS_PATH" > "$RUN_CWD/opencode.json"
    echo "[driver] seeded project config $RUN_CWD/opencode.json from recipe settings" >&2
  fi
fi

OPENCODE_DB="$HOME/.local/share/opencode/opencode.db"
if [[ ! -f "$OPENCODE_DB" ]]; then
  echo "opencode database not found at $OPENCODE_DB — is opencode installed?" >&2
  exit 1
fi

DEADLINE=$(( $(date +%s) + TIMEOUT_S ))
EXIT_REASON="ok"

# Per-slot session ids (1-based; index 0 unused). Each slot is an
# independent `opencode run` chain in RUN_CWD. The initial session is slot 1;
# a start_session step allocates the next slot with an empty id, so its first
# send mints a fresh ses_ row. SESSION_ID is the ACTIVE slot's id — a cache of
# SES_IDS[$ACTIVE], kept in sync via the {"session":N} switch and run_send's
# capture. Single-slot (the common case) leaves N_SLOTS=1 / ACTIVE=1, so the
# headless flow is byte-for-byte unchanged for every other opencode cell.
SES_IDS=("")
N_SLOTS=1
ACTIVE=1
SESSION_ID=""

remaining_seconds() {
  local now
  now=$(date +%s)
  if (( now >= DEADLINE )); then
    echo 0
  else
    echo $((DEADLINE - now))
  fi
}

# ---------------------------------------------------------------------------
# LIVE-TUI path (slash commands / session reset)
# ---------------------------------------------------------------------------
# Headless `opencode run` cannot deliver in-REPL slash commands — it stores
# `/new` as ordinary prompt text (proven 2026-05-26). The `session-reset`
# scenario needs a live `/new` that retires the current ses_ row and mints a
# fresh one under the SAME process, so when a recipe carries a `reset_session`
# (or `slash`) step we drive opencode's TUI under tmux instead, exactly like
# the codex/pi/aider drivers. Session identity, the per-cwd lookup, and the
# transcript export are the SAME as the headless path; only the input
# mechanism (tmux send-keys) and turn detection (poll the SQLite store) differ.
# Emits the multi-session contract (session.uuids / transcript.paths) so
# run-cell.sh + curate-lifecycle-fixture.sh include BOTH the pre- and
# post-reset sessions, the same shape codex's reset_session produces.
run_live() {
  set +e   # this path manages failures via EXIT_REASON, not set -e aborts
  command -v tmux >/dev/null 2>&1 || {
    echo "[driver] live mode requires tmux (not found)" >&2
    echo "nonzero(2)" > "$STAGING/driver.exit-reason"
    exit 1
  }

  local SESSION="ocdrv-$$-$(date +%s)"
  # Per-turn baseline: the terminal-step-finish high-water mark captured at the
  # moment a send fires. oc_wait_turn waits for the terminal count to strictly
  # exceed it — exactly one NEW `stop` per turn. (A raw all-step count compared
  # to a turn counter over-counts: opencode emits a step-finish PER STEP, with
  # reason="tool-calls" for every mid-turn tool iteration, so a single
  # multi-step turn would push the total past the turn counter and make the
  # NEXT wait_turn return immediately on an unprocessed prompt.)
  local TURN_BASELINE=0
  # Set once oc_sigkill has killed the live opencode process: there is no longer
  # a turn to await, so oc_wait_turn returns immediately instead of polling a
  # now-static counter to the deadline. oc_restart clears it (fresh process).
  local LIVE_DEAD=0

  echo "[driver] live mode: launching opencode TUI (tmux=$SESSION, cwd=$RUN_CWD)" >&2
  tmux kill-session -t "$SESSION" 2>/dev/null || true
  tmux new-session -d -s "$SESSION" -x 200 -y 50 -c "$RUN_CWD" "opencode" \
    >>"$DRIVER_LOG.stdout" 2>>"$DRIVER_LOG.stderr"
  # Always tear the TUI down, even on an error/timeout exit below.
  trap 'tmux kill-session -t "$SESSION" 2>/dev/null || true' EXIT

  # TERMINAL step-finish parts across OUR cwd's top-level sessions = completed
  # turns. opencode emits a step-finish per STEP, so only the terminal one —
  # reason="stop" — marks a turn boundary (matching parser.go's
  # parseStepFinish: `case "stop": ev.EventType = "turn_done"`). Mid-turn
  # tool-call iterations carry reason="tool-calls" and must NOT count.
  # Counting across all of the cwd's sessions (not just the active one) makes
  # the high-water mark survive the reset boundary: the post-/new session's
  # first terminal step-finish simply bumps the same total.
  # Count TERMINAL step-finish parts across OUR cwd's top-level sessions =
  # completed turns. opencode emits a step-finish per STEP; a mid-turn tool
  # iteration carries reason="tool-calls" and must NOT count, while a turn ENDS
  # on any of stop / interrupted / length / error / content-filter. This set is
  # exactly what parser.go's parseStepFinish maps to turn_done (an unknown
  # reason is treated as a mid-turn assistant_message, NOT turn-ending, so it is
  # excluded here too). Counting the full terminal set — not just "stop" — means
  # an ESC-interrupted turn (reason=interrupted), a context-overflow turn
  # (length), or an errored turn (error) settles the daemon AND bumps this
  # counter, so oc_wait_turn / oc_interrupt detect the boundary instead of
  # polling to the deadline. Counting across all of the cwd's sessions (not just
  # the active one) keeps the high-water mark monotonic across a /new reset.
  oc_turn_total() {
    sqlite3 -cmd ".timeout 5000" "$OPENCODE_DB" \
      "SELECT count(*) FROM part p JOIN session s ON p.session_id = s.id
       WHERE s.directory = '$RUN_CWD_SQL' AND (s.parent_id IS NULL OR s.parent_id = '')
         AND json_extract(p.data,'\$.type') = 'step-finish'
         AND json_extract(p.data,'\$.reason')
             IN ('stop','interrupted','length','error','content-filter');" 2>/dev/null || echo 0
  }

  # oc_interrupted_total — like oc_turn_total but counts ONLY step-finish parts
  # whose reason is exactly "interrupted". oc_interrupt uses this (not the broad
  # terminal set) so it settles on a GENUINE mid-stream cancel and NOT on a turn
  # that finished naturally on `stop` (or `length`/`error`/`tool-calls`). The
  # local provider can stall pre-stream so a bounded ESC lands in the dead zone
  # and the turn completes on `stop`; counting the broad set there false-settled
  # the interrupt (#492). Counting interrupted-only makes a no-op ESC fail loudly.
  oc_interrupted_total() {
    sqlite3 -cmd ".timeout 5000" "$OPENCODE_DB" \
      "SELECT count(*) FROM part p JOIN session s ON p.session_id = s.id
       WHERE s.directory = '$RUN_CWD_SQL' AND (s.parent_id IS NULL OR s.parent_id = '')
         AND json_extract(p.data,'\$.type') = 'step-finish'
         AND json_extract(p.data,'\$.reason') = 'interrupted';" 2>/dev/null || echo 0
  }

  # Readiness: opencode swallows keystrokes during its boot splash, so wait
  # for the input affordance to render before the first send, then a margin.
  local waited=0 ready=0
  while (( $(remaining_seconds) > 0 )) && (( waited < 40 )); do
    if tmux capture-pane -t "$SESSION" -p 2>/dev/null | grep -q "Ask anything"; then
      ready=1; break
    fi
    sleep 1; waited=$((waited + 1))
  done
  if (( ready == 0 )); then
    echo "[driver] live: opencode TUI never rendered an input prompt" >&2
    EXIT_REASON="readiness_timeout"
  else
    sleep 3
  fi

  # oc_composer_holds <prefix> → 0 if <prefix> is still sitting in the input
  # composer, 1 otherwise. The composer is the bottom box of the TUI (a ┃-margin
  # region above the model footer + ╹▀▀▀ top-border); the typed text lands on a
  # ┃-prefixed line there until Enter flushes it — submitted text moves UP into
  # the transcript (far above) or, for a local slash, vanishes. So we look ONLY
  # at the bottom slice (last 12 lines) for the prefix: that excludes the
  # transcript echo of an earlier prompt, which persists scrolled off the top
  # but never re-enters the composer box. The box is 200 cols wide (-x 200) so a
  # 24-char prefix never wraps; the prompt may carry a leading glyph so we match
  # the prefix substring, not a line anchor.
  oc_composer_holds() {
    tmux capture-pane -t "$SESSION" -p 2>/dev/null | tail -12 \
      | grep -qF "${1:0:24}"
  }

  # oc_flush <prefix> → press Enter and confirm the composer actually FLUSHED
  # (the just-typed text left the input box). Under the slow local model an
  # Enter-submitted prompt can linger; without this confirmation the NEXT
  # oc_send/slash types into a non-empty composer and the two inputs COALESCE
  # into one merged user message (and a following slash is swallowed as literal
  # text, never executing). Bounded by remaining_seconds with a 30s cap; if it
  # has not cleared by then, re-press Enter ONCE and proceed regardless.
  oc_flush() {
    local prefix="$1" waited=0 cap=30
    tmux send-keys -t "$SESSION" Enter
    while (( $(remaining_seconds) > 0 )) && (( waited < cap )); do
      sleep 1; waited=$((waited + 1))
      oc_composer_holds "$prefix" || return 0
      if (( waited == cap / 2 )); then
        echo "[driver] live: composer still holds input after ${waited}s, re-pressing Enter" >&2
        tmux send-keys -t "$SESSION" Enter
      fi
    done
    echo "[driver] live: composer did not confirm flush within ${cap}s; proceeding" >&2
    return 0
  }

  oc_send() {
    local text="$1" tries=0
    while (( tries < 3 )); do
      tmux send-keys -t "$SESSION" -l -- "$text"
      sleep 0.5
      # Confirm the keystrokes landed (boot splash can still eat them). Scan the
      # FULL pane, not just the composer slice: on the first send the composer
      # is the centered welcome screen (the "Ask anything" affordance sits
      # mid-pane, not in the bottom box), so a bottom-only match would miss it
      # and retype. The input box is 200 cols wide so a 24-char prefix never
      # wraps.
      if tmux capture-pane -t "$SESSION" -p 2>/dev/null | grep -qF "${text:0:24}"; then
        break
      fi
      tries=$((tries + 1))
      echo "[driver] live: send did not register (try $tries), retrying" >&2
      sleep 1
    done
    # Snapshot the terminal-step-finish high-water mark BEFORE submitting; the
    # matching wait_turn waits for the count to strictly exceed this, i.e. for
    # the ONE new terminal step-finish this turn will produce (stop / length /
    # error / content-filter — any turn-ending reason). Captured pre-Enter (not
    # in wait_turn, and not after the flush) so that (a) a multi-step turn
    # already in flight can't satisfy the next turn's wait, and (b) a sub-second
    # turn that completes before oc_flush confirms the composer cleared does not
    # get folded INTO its own baseline — which would make its wait_turn need an
    # extra terminal step-finish that never comes and time out.
    TURN_BASELINE=$(oc_turn_total)
    # Press Enter and wait for the composer to FLUSH (text submitted + cleared)
    # before returning — a lingering composer would let the next input merge
    # into this one. (See oc_flush.)
    oc_flush "$text"
    echo "[driver] live send (terminal baseline=$TURN_BASELINE): ${text:0:60}" >&2
  }

  oc_wait_turn() {
    local now
    # The live opencode process was SIGKILLed (oc_sigkill) and not restarted —
    # no further turn can land, so don't burn the budget polling a dead store.
    if (( LIVE_DEAD == 1 )); then
      echo "[driver] live wait_turn: process killed, nothing to await" >&2
      return 0
    fi
    while (( $(remaining_seconds) > 0 )); do
      now=$(oc_turn_total)
      if (( now > TURN_BASELINE )); then
        echo "[driver] live wait_turn: terminal step-finish total=$now (> baseline $TURN_BASELINE)" >&2
        # Advance the baseline to the turn we just observed so the NEXT
        # wait_turn waits for the FOLLOWING turn instead of returning instantly
        # on this same count. This is what lets a queued mid_turn_send turn be
        # awaited (send sets baseline=B; wait #1 → B+1, baseline:=B+1; the queued
        # turn → B+2 > B+1, wait #2 returns correctly) and stops two consecutive
        # wait_turns from both settling on one turn.
        TURN_BASELINE=$now
        return 0
      fi
      sleep 2
    done
    echo "[driver] live wait_turn: timeout (total=$(oc_turn_total), needed > baseline $TURN_BASELINE)" >&2
    EXIT_REASON="timeout"
    return 1
  }

  # oc_mid_turn_send — type a SECOND message + Enter into the composer WHILE a
  # turn is still in flight. opencode's TUI silently QUEUES that message (its
  # documented default) and delivers it as a NEW turn only after the current one
  # finishes; the queued text is NOT written to SQLite until consumed, so the
  # daemon stays `working` with no flicker (the queued-mid-turn-message cell).
  # Reuse the composer landing/flush check from oc_send (the composer accepts
  # input during a turn), but DELIBERATELY skip the turn-detection bookkeeping:
  # no TURN_BASELINE snapshot/bump here, because this send does not start its own
  # observable turn boundary at submit time. A SUBSEQUENT wait_turn waits for the
  # queued message to run as the next turn (its terminal step-finish bumps the
  # count past whatever baseline the preceding oc_send set). The flush confirms
  # the queued text left the composer so a following step can't coalesce into it.
  oc_mid_turn_send() {
    local text="$1" tries=0
    while (( tries < 3 )); do
      tmux send-keys -t "$SESSION" -l -- "$text"
      sleep 0.5
      if tmux capture-pane -t "$SESSION" -p 2>/dev/null | grep -qF "${text:0:24}"; then
        break
      fi
      tries=$((tries + 1))
      echo "[driver] live: mid-turn send did not register (try $tries), retrying" >&2
      sleep 1
    done
    # NO TURN_BASELINE snapshot/bump — the queued message does not open its own
    # turn at submit time; the next wait_turn rides the preceding send's baseline.
    oc_flush "$text"
    echo "[driver] live mid_turn_send (queued, no baseline bump): ${text:0:60}" >&2
  }

  # oc_interrupt — cancel the in-flight turn by sending a bare Escape to the
  # live TUI (opencode binds Escape to "interrupt"), exactly like the
  # claudecode/codex step_interrupt arms. A genuinely cancelled turn lands a
  # step-finish with reason="interrupted" (parser.go maps it to turn_done).
  #
  # We settle ONLY on that interrupted-only counter crossing its baseline — NOT
  # on the broad terminal counter (oc_turn_total). The distinction is the whole
  # point of this cell (#492): the local provider can stall ~2min pre-stream, so
  # a bounded ESC lands in the dead zone (no in-flight stream to cancel) and the
  # turn then completes NATURALLY on reason=stop. The old code settled on ANY
  # terminal step-finish, so it false-passed that no-op `stop` as a successful
  # interrupt and a recording proving the WRONG thing could be promoted. Now:
  #   - a real interrupted part appears  → settle (return 0)
  #   - a non-interrupted terminal part appears first (stop/length/error) → the
  #     ESC was a no-op; FAIL LOUDLY (return 1) so the run is not promoted
  #   - neither within the budget        → timeout (return 1)
  # On settle, ADVANCE the shared TURN_BASELINE to the current terminal total so
  # a following oc_wait_turn waits for the NEXT turn rather than re-detecting
  # this interrupted one (the interrupted step-finish already bumped the count).
  oc_interrupt() {
    local ibase tbase inow tnow
    ibase=$(oc_interrupted_total)
    tbase=$(oc_turn_total)
    tmux send-keys -t "$SESSION" Escape
    echo "[driver] live interrupt: sent Escape (interrupted baseline=$ibase, terminal baseline=$tbase)" >&2
    while (( $(remaining_seconds) > 0 )); do
      inow=$(oc_interrupted_total)
      if (( inow > ibase )); then
        echo "[driver] live interrupt: settled on a real step-finish reason=interrupted (interrupted total=$inow > baseline $ibase)" >&2
        TURN_BASELINE=$(oc_turn_total)
        return 0
      fi
      tnow=$(oc_turn_total)
      if (( tnow > tbase )); then
        echo "[driver] live interrupt: FAILED — the turn ended on a NON-interrupted terminal step-finish (terminal total=$tnow > baseline $tbase) with NO step-finish reason=interrupted: the Escape was a no-op (model finished naturally / stalled pre-stream). Not promotable." >&2
        EXIT_REASON="interrupt_noop"
        return 1
      fi
      sleep 2
    done
    echo "[driver] live interrupt: timeout (interrupted total=$(oc_interrupted_total), needed > baseline $ibase; no interrupted part landed within the budget)" >&2
    EXIT_REASON="timeout"
    return 1
  }

  # oc_keys — send a raw tmux key sequence (NOT literal text) for navigating
  # picker UIs (Up/Down/Enter/Escape). Each space-separated token is one tmux
  # key event; no implicit Enter. Mirrors the claudecode/codex `keys` arm.
  oc_keys() {
    local keys="$1"
    # $keys is deliberately word-split below (each token = one tmux key event),
    # so reject a literal ';' (tmux treats a ';' argument as a COMMAND separator,
    # letting a recipe smuggle an extra send-keys/run-shell) and shell glob
    # metacharacters (the unquoted expansion would otherwise glob against the
    # cwd). Recipe key lists are plain key names (Up/Down/Enter/Escape/…) + text.
    if [[ "$keys" == *';'* || "$keys" == *'*'* || "$keys" == *'?'* || "$keys" == *'['* ]]; then
      echo "[driver] ERROR: invalid keys value '$keys' (contains ';' or a glob metacharacter)" >&2
      EXIT_REASON="nonzero(2)"
      return 1
    fi
    # shellcheck disable=SC2086 — intentional word-splitting of the key list
    tmux send-keys -t "$SESSION" $keys
    echo "[driver] live keys: $keys" >&2
    sleep 0.5
  }

  # /new clears the conversation; the fresh ses_ row is minted lazily on the
  # NEXT send (same as the initial session, and codex's post-/clear rollout).
  # No baseline bump — the reset itself is not a turn, and the next oc_send
  # snapshots the (monotonic, reset-surviving) terminal count for its own turn.
  oc_reset() {
    echo "[driver] live reset_session: delivering /new" >&2
    tmux send-keys -t "$SESSION" -l -- "/new"
    sleep 0.5
    # /new is a local command (no model turn), but it is still a slash command:
    # confirm the composer FLUSHED (oc_flush presses Enter, then waits for the
    # input box to clear) so a lingering "/new" cannot merge with the next send
    # and be swallowed as literal text — the same discipline the `slash` arm
    # uses. Without it, under the slow local model the reset can fail to register
    # and session-reset's two-session contract collapses to one.
    oc_flush "/new"
    sleep 1
  }

  # oc_opencode_pid — the live opencode PID the daemon is tracking. opencode's
  # PID discovery (opencode/pid.go → DiscoverPIDByCWD) matches a process named
  # exactly `opencode` whose CWD == $RUN_CWD, so mirror that lookup here: the
  # SIGKILL MUST land on the very PID the daemon watches, or it never observes
  # process_exited. Primary: pgrep the `opencode` whose cwd is $RUN_CWD (lsof
  # the process's cwd FD). Fallback: the opencode descendant of this slot's
  # tmux pane (in case tmux wrapped the command in a shell) — and only if that
  # also fails, the pane PID itself, so the kill can't merely orphan opencode
  # while it keeps the parent+child sessions alive.
  oc_opencode_pid() {
    local pid="" p pane_pid comm
    # Common case: `tmux new-session ... "opencode"` execs the single-word
    # command directly, so the PANE process IS opencode — exactly the PID the
    # daemon's DiscoverPIDByCWD tracks. Use it without guessing or scanning.
    pane_pid=$(tmux list-panes -t "$SESSION" -F '#{pane_pid}' 2>/dev/null | head -1)
    if [[ -n "$pane_pid" ]]; then
      comm=$(ps -o comm= -p "$pane_pid" 2>/dev/null | tr -d ' ')
      if [[ "$comm" == *opencode* ]]; then
        printf '%s' "$pane_pid"; return 0
      fi
    fi
    # Fallback (tmux wrapped opencode in a shell): the opencode process whose
    # cwd is RUN_CWD. Parse lsof's NAME field with `-Fn` (emits one `n<path>`
    # line) so a cwd containing spaces matches intact — `awk '{print $NF}'`
    # would split it. Only ever return a process actually named `opencode`
    # (never the wrapper shell), so a miss yields empty and oc_sigkill no-ops
    # rather than kill -9'ing the pane shell and leaving opencode alive.
    for p in $(pgrep -x opencode 2>/dev/null); do
      if lsof -a -p "$p" -d cwd -Fn 2>/dev/null | grep -qxF "n$RUN_CWD"; then
        pid="$p"; break
      fi
    done
    printf '%s' "$pid"
  }

  # oc_sigkill — kill -9 the live opencode process mid-turn (the SIGKILL
  # counterpart to a graceful exit). opencode runs foreground Task subagents as
  # CHILD SQLite sessions of the SAME process (session.parent_id), so killing
  # the one opencode process orphans BOTH the parent and any in-flight child —
  # the daemon's PIDManager sees the PID vanish and its transport-agnostic
  # CleanupZombies / deleteWithChildren sweep reaps the orphaned parent+child
  # (pure repo.Delete + broadcast, no SetState → no spurious working
  # transition, #321). Mirrors the claudecode/codex step_sigkill: target the
  # daemon's PID, leave the dead pane for teardown — the kill alone produces
  # process_exited. (For the eventual subagent-orphan-cleanup recipe: send a
  # Task-spawning prompt, sleep until the child ses_ row exists in opencode.db
  # with parent_id set AND a turn is in flight, then sigkill.)
  oc_sigkill() {
    local pid; pid=$(oc_opencode_pid)
    if [[ -n "$pid" ]]; then
      kill -9 "$pid" 2>/dev/null || true
      echo "[driver] live sigkill: killed opencode PID $pid (cwd=$RUN_CWD)" >&2
    else
      echo "[driver] live sigkill: no opencode PID found for cwd=$RUN_CWD" >&2
    fi
    # The live process is gone: a subsequent wait_turn must not poll for a turn
    # that can never land (oc_restart clears this when it relaunches).
    LIVE_DEAD=1
    sleep 1
  }

  # oc_restart — SIGKILL the live opencode (orphaning parent+child for the
  # daemon's zombie sweep, exactly as oc_sigkill) and then relaunch a FRESH
  # opencode TUI in the SAME cwd so the recipe can continue against a new
  # process. Mirrors claudecode/codex step_restart (end the active session,
  # start a fresh one). The post-restart session is minted lazily on the next
  # send — the run_live epilogue enumerates EVERY top-level ses_ under the cwd,
  # so both the pre-restart (now orphaned/reaped) and post-restart sessions are
  # captured in the multi-session contract (session.uuids / transcript.paths),
  # the same shape codex's restart produces. The child (subagent) sessions are
  # NOT in that contract by design — they carry parent_id and the daemon reaps
  # them as zombies; the contract enumerates top-level rows only.
  oc_restart() {
    oc_sigkill
    LIVE_DEAD=0   # a fresh process is about to come up — re-enable wait_turn
    tmux kill-session -t "$SESSION" 2>/dev/null || true
    sleep 1
    echo "[driver] live restart: relaunching opencode TUI (tmux=$SESSION, cwd=$RUN_CWD)" >&2
    tmux new-session -d -s "$SESSION" -x 200 -y 50 -c "$RUN_CWD" "opencode" \
      >>"$DRIVER_LOG.stdout" 2>>"$DRIVER_LOG.stderr"
    # Re-wait the input affordance before any subsequent send (same readiness
    # gate as the initial launch; opencode swallows keystrokes during boot).
    local waited=0
    while (( $(remaining_seconds) > 0 )) && (( waited < 40 )); do
      if tmux capture-pane -t "$SESSION" -p 2>/dev/null | grep -q "Ask anything"; then
        break
      fi
      sleep 1; waited=$((waited + 1))
    done
    sleep 3
  }

  if [[ "$EXIT_REASON" == "ok" ]]; then
    local STEP_COUNT i STEP TYPE
    STEP_COUNT=$(jq 'length' <<<"$SCRIPT_JSON")
    for (( i = 0; i < STEP_COUNT; i++ )); do
      STEP=$(jq -c ".[$i]" <<<"$SCRIPT_JSON")
      TYPE=$(jq -r '.type' <<<"$STEP")
      case "$TYPE" in
        live)
          # No-op marker: its mere presence in script[] forces this recipe onto
          # the long-lived run_live path (see router below), so a PLAIN
          # send/wait_turn session keeps ONE opencode process alive across every
          # turn instead of spawning a fresh `opencode run` per turn. That
          # avoids the headless per-run process-exit-before-poll race that
          # truncates the final settle on long agentic sessions.
          echo "[driver] live marker: long-lived run_live path" >&2
          ;;
        send)
          oc_send "$(jq -r '.text' <<<"$STEP")"
          ;;
        mid_turn_send)
          # Queue a SECOND message into the composer WHILE the current turn is
          # still running — opencode buffers it and runs it as the NEXT turn.
          # No baseline bump; a SUBSEQUENT wait_turn detects the queued turn.
          oc_mid_turn_send "$(jq -r '.text' <<<"$STEP")"
          ;;
        wait_turn)
          oc_wait_turn || break
          ;;
        reset_session)
          oc_reset
          ;;
        interrupt)
          # Cancel the in-flight turn (bare Escape) and wait for the
          # interrupted settle. Reuses the turn-detection high-water mark but
          # accepts reason IN ('stop','interrupted') as turn-ending.
          oc_interrupt || break
          ;;
        keys)
          oc_keys "$(jq -r '.keys' <<<"$STEP")" || break
          ;;
        sigkill)
          # kill -9 the live opencode process mid-turn — orphans the parent and
          # any in-flight child (subagent) session of the same process; the
          # daemon's zombie sweep reaps both.
          oc_sigkill
          ;;
        restart)
          # SIGKILL the live opencode (orphaning parent+child) then relaunch a
          # fresh TUI in the same cwd so the recipe can continue.
          oc_restart
          ;;
        slash)
          local s; s=$(jq -r '.text // .command // empty' <<<"$STEP")
          tmux send-keys -t "$SESSION" -l -- "$s"; sleep 0.5
          # A slash must go into an EMPTY composer and confirm it submitted
          # before the next step runs — otherwise it merges with a lingering
          # prompt and is swallowed as literal text instead of executing. A
          # local no-LLM slash (/undo, /help) produces no model turn, so we
          # confirm via composer-clear (oc_flush), not a turn count.
          oc_flush "$s"
          echo "[driver] live slash: $s" >&2
          ;;
        sleep)
          local secs; secs=$(jq -r '.seconds // empty' <<<"$STEP")
          if ! [[ "$secs" =~ ^[0-9]+(\.[0-9]+)?$ ]]; then
            echo "[driver] ERROR: sleep step missing/non-numeric: $STEP" >&2
            EXIT_REASON="nonzero(2)"; break
          fi
          echo "[driver] live sleep ${secs}s" >&2; sleep "$secs"
          ;;
        *)
          echo "[driver] ERROR: unknown step type '$TYPE'" >&2
          EXIT_REASON="nonzero(2)"; break
          ;;
      esac
    done
  fi

  # Settle so the daemon's recorder observes the final state, then drop the TUI.
  sleep 1
  tmux kill-session -t "$SESSION" 2>/dev/null || true
  trap - EXIT

  # Enumerate OUR cwd's top-level sessions chronologically, skipping any empty
  # stub (a session row with no message). [old, new] after one reset.
  local SIDS=()
  while IFS= read -r sid; do
    [[ -n "$sid" ]] && SIDS+=("$sid")
  done < <(sqlite3 -cmd ".timeout 5000" "$OPENCODE_DB" \
    "SELECT s.id FROM session s
     WHERE s.directory = '$RUN_CWD' AND (s.parent_id IS NULL OR s.parent_id = '')
       AND EXISTS (SELECT 1 FROM message m WHERE m.session_id = s.id)
     ORDER BY s.time_created ASC;" 2>/dev/null)

  : > "$STAGING/session.uuids"
  : > "$STAGING/transcript.paths"
  local n=0 first_uuid="" first_path="" sid tpath
  for sid in ${SIDS[@]+"${SIDS[@]}"}; do
    n=$((n + 1))
    tpath="$STAGING/opencode-transcript.$n.jsonl"
    : > "$tpath"
    sqlite3 "$OPENCODE_DB" >>"$tpath" <<SQL
.timeout 5000
.mode list
.separator ""
SELECT json_set(
  p.data,
  '\$._role',  json_extract(m.data, '\$.role'),
  '\$._cwd',   s.directory,
  '\$._ts',    p.time_updated,
  '\$._model', json_extract(m.data, '\$.model.modelID'),
  '\$._error', json(json_extract(m.data, '\$.error'))
)
FROM part p
JOIN message m ON p.message_id = m.id
JOIN session s ON p.session_id = s.id
WHERE p.session_id = '$sid'
ORDER BY p.time_created ASC, p.id ASC;
SQL
    echo "$sid" >> "$STAGING/session.uuids"
    echo "$tpath" >> "$STAGING/transcript.paths"
    if [[ -z "$first_uuid" ]]; then first_uuid="$sid"; first_path="$tpath"; fi
    echo "[driver] live session #$n: $sid -> $tpath ($(wc -l <"$tpath" | tr -d ' ') parts)" >&2
  done

  if [[ -z "$first_uuid" && "$EXIT_REASON" == "ok" ]]; then
    echo "[driver] live: no session rows materialized under $RUN_CWD" >&2
    EXIT_REASON="nonzero(1)"
  fi

  echo "$EXIT_REASON" > "$STAGING/driver.exit-reason"
  echo "${first_uuid}" > "$STAGING/session.uuid"
  echo "${first_path}" > "$STAGING/transcript.path"

  {
    echo "=== live opencode driver ==="
    echo "exit reason : $EXIT_REASON"
    echo "sessions    : ${SIDS[*]:-<none>}"
    echo "primary     : ${first_uuid:-<none>}"
  } > "$DRIVER_LOG"

  echo "drive-opencode-interactive(live): $EXIT_REASON (sessions=$n, primary=${first_uuid:-<none>})"
  case "$EXIT_REASON" in
    ok) exit 0 ;;
    *)  exit 1 ;;
  esac
}

run_send() {
  local text="$1"
  # Optional per-step model: when the send step carries a `model` field, thread
  # `opencode run -m <provider/model>` so THIS turn runs on the named model
  # instead of the config default. This is the model-select primitive
  # (gap:model-select): a real A->B model switch across turns within one
  # --session chain — the daemon reads per-turn message.data.model.modelID from
  # its SQLite store (latest-non-empty-wins, opencode/metrics.go), so the switch
  # is observable. Omitting the field (every other cell) passes NO -m flag and
  # keeps the single-config-default behavior byte-for-byte unchanged.
  local model="${2:-}"
  local args=()
  if [[ -n "$SESSION_ID" ]]; then
    args+=(--session "$SESSION_ID")
  fi
  if [[ -n "$model" ]]; then
    args+=(-m "$model")
  fi

  local remaining
  remaining=$(remaining_seconds)
  if (( remaining <= 0 )); then
    EXIT_REASON="timeout"
    return 1
  fi

  echo "[driver] send (session=${SESSION_ID:-<new>}, model=${model:-<default>}, remaining=${remaining}s): $text" >&2
  set +e
  ( cd "$RUN_CWD" && \
    timeout --signal=SIGINT --kill-after=10 "$remaining" \
      opencode run --format default ${args[@]+"${args[@]}"} -- "$text" \
      >>"$DRIVER_LOG.stdout" 2>>"$DRIVER_LOG.stderr" )
  local rc=$?
  set -e

  case "$rc" in
    0)   ;;
    124) EXIT_REASON="timeout"; return 1 ;;
    137) EXIT_REASON="killed";  return 1 ;;
    *)   EXIT_REASON="nonzero($rc)"; return 1 ;;
  esac

  # Capture the session id after the first send. The session row is
  # created by `opencode run` and keyed on directory = $RUN_CWD; order
  # by time_created DESC so retries reusing a stale staging dir pick
  # the NEW session, not a leftover row whose time_updated may briefly
  # outrank the fresh row before its first part lands.
  #
  # `parent_id IS NULL OR parent_id = ''` excludes subagent (child) sessions,
  # which opencode keys to the SAME directory as their parent — without the
  # filter a turn that spawns a subagent could capture the child id instead of
  # the parent the export below intends.
  #
  # `.timeout 5000` lets the read wait out a transient SQLITE_BUSY rather than
  # failing the bare `SESSION_ID=$(...)` under `set -e` — opencode is a
  # concurrent WAL writer (especially under the cross-adapter shared-cwd load).
  #
  # Concurrency note: with start_session, two slots share RUN_CWD. The capture
  # only fires for the ACTIVE slot when its id is still empty (its first send,
  # which ran with NO --session and therefore minted the newest top-level row).
  # ORDER BY time_created DESC LIMIT 1 picks that just-minted row; slot 1's id
  # was captured on ITS first send and is now non-empty, so it is never
  # re-looked-up and the two chains never alias. The captured id is mirrored
  # into SES_IDS[$ACTIVE] so the end-of-run epilogue exports every slot.
  if [[ -z "$SESSION_ID" ]]; then
    SESSION_ID=$(sqlite3 -cmd ".timeout 5000" "$OPENCODE_DB" \
      "SELECT id FROM session WHERE directory = '$RUN_CWD_SQL' AND (parent_id IS NULL OR parent_id = '') ORDER BY time_created DESC LIMIT 1;")
    if [[ -z "$SESSION_ID" ]]; then
      echo "[driver] WARN: no session row found for cwd=$RUN_CWD" >&2
    else
      SES_IDS[$ACTIVE]="$SESSION_ID"
      echo "[driver] captured session_id=$SESSION_ID (slot $ACTIVE)" >&2
    fi
  fi
  return 0
}

# start_session — begin a SECOND independent `opencode run` chain in RUN_CWD.
# Mirror the active slot's id back, allocate the next slot with an EMPTY id,
# and make it active. The next `send` runs with no --session (empty id) and so
# mints a fresh ses_ row keyed to the same cwd; run_send captures it into the
# new slot. The previous slot's chain is untouched (its id stays in SES_IDS),
# so a later {"session":1} resumes it via --session <ses1>. This is the
# headless analogue of claudecode's step_start_session (alloc_slot + a fresh
# session), staying on the deterministic headless path rather than the TUI.
run_start_session() {
  SES_IDS[$ACTIVE]="$SESSION_ID"            # persist the active slot's id
  N_SLOTS=$((N_SLOTS + 1))
  SES_IDS[$N_SLOTS]=""                      # new slot: empty → next send mints
  ACTIVE=$N_SLOTS
  SESSION_ID=""                             # active view follows the new slot
  echo "[driver] start_session: concurrent slot #$ACTIVE in $RUN_CWD (next send mints a fresh ses_)" >&2
}

# Route: a `reset_session` / `slash` / `interrupt` / `keys` / `restart` /
# `sigkill` / `mid_turn_send` step needs the live opencode TUI — headless
# `opencode run` treats slash commands as ordinary prompt text and offers no
# in-flight signal channel (an interrupt = a bare Escape to the running TUI;
# `keys` = raw key navigation; restart/sigkill must kill a long-lived TUI
# process so the daemon observes process_exited — a headless `opencode run`
# already self-exits per turn, so there is no parent process to orphan a child
# against; mid_turn_send queues a 2nd message into the composer DURING an
# in-flight turn, which only the persistent TUI can buffer — a per-turn headless
# run has no composer to type into while it blocks). All other opencode cells
# stay on the simpler, deterministic headless path — including start_session,
# which is just a SECOND headless `opencode run` chain (two interleaved chains,
# no TUI), so it does NOT force run_live.
LIVE_ROUTE=$(jq -r 'any(.[]?; .type == "reset_session" or .type == "slash" or .type == "interrupt" or .type == "keys" or .type == "restart" or .type == "sigkill" or .type == "mid_turn_send" or .type == "live")' <<<"$SCRIPT_JSON")
# Guard: the multi-slot primitives (start_session and the {"session":N} inline
# switch) are implemented ONLY on the headless path below — run_live has no slot
# bookkeeping. A recipe that mixes them with a live-TUI-only step would route to
# run_live and then abort on the first start_session/session step ("unknown step
# type"), with the slots half-applied. Fail fast with a clear reason instead;
# such a behavior needs splitting into separate cells (or porting slots to run_live).
if [[ "$LIVE_ROUTE" == "true" ]] \
   && [[ "$(jq -r 'any(.[]?; .type == "start_session" or (.session != null))' <<<"$SCRIPT_JSON")" == "true" ]]; then
  echo "[driver] ERROR: recipe mixes a live-TUI step (reset_session/slash/interrupt/keys/restart/sigkill/mid_turn_send/live) with a headless-only multi-slot step (start_session/{\"session\":N}); these cannot run together. Split into separate cells." >&2
  echo "nonzero(2)" > "$STAGING/driver.exit-reason"
  exit 1
fi
if [[ "$LIVE_ROUTE" == "true" ]]; then
  run_live   # drives the TUI under tmux and exits; never returns here.
fi

# Iterate steps.
STEP_COUNT=$(jq 'length' <<<"$SCRIPT_JSON")
for (( i = 0; i < STEP_COUNT; i++ )); do
  STEP=$(jq -c ".[$i]" <<<"$SCRIPT_JSON")
  TYPE=$(jq -r '.type' <<<"$STEP")

  # Optional inline session target: switch the active slot to N (1-based)
  # before executing the step — e.g. route a later send back to slot 1 while
  # slot 2 stays alive. start_session is exempt (it allocates its own slot).
  # Persist the current slot's id, then load the target's. A target must
  # already exist. Same contract as the claudecode driver's {"session":N}.
  TGT=$(jq -r '.session // empty' <<<"$STEP")
  if [[ -n "$TGT" && "$TYPE" != "start_session" && "$TGT" != "$ACTIVE" ]]; then
    if [[ "$TGT" =~ ^[0-9]+$ && "$TGT" -ge 1 && "$TGT" -le "$N_SLOTS" ]]; then
      SES_IDS[$ACTIVE]="$SESSION_ID"
      ACTIVE="$TGT"
      SESSION_ID="${SES_IDS[$ACTIVE]}"
      echo "[driver] switch -> session slot $ACTIVE (id=${SESSION_ID:-<new>})" >&2
    else
      echo "[driver] ERROR: invalid session slot '$TGT' (have $N_SLOTS)" >&2
      EXIT_REASON="nonzero(2)"
      break
    fi
  fi

  case "$TYPE" in
    send)
      TEXT=$(jq -r '.text' <<<"$STEP")
      # Optional `model` field → run THIS turn on the named provider/model via
      # `opencode run -m`; absent → config default (unchanged for every other
      # cell). This is the gap:model-select unblock for model-switch-midsession.
      MODEL=$(jq -r '.model // empty' <<<"$STEP")
      run_send "$TEXT" "$MODEL" || break
      ;;
    wait_turn)
      # opencode run blocks until the turn ends — wait_turn is a no-op.
      :
      ;;
    sleep)
      SECONDS_=$(jq -r '.seconds // empty' <<<"$STEP")
      # Reject missing/non-numeric values so an authoring typo
      # (`{"type":"sleep"}` without seconds) doesn't silently abort the
      # whole script under `set -e` with no exit-reason file written.
      if ! [[ "$SECONDS_" =~ ^[0-9]+(\.[0-9]+)?$ ]]; then
        echo "[driver] ERROR: sleep step missing or non-numeric 'seconds': $STEP" >&2
        EXIT_REASON="nonzero(2)"
        break
      fi
      echo "[driver] sleep ${SECONDS_}s" >&2
      sleep "$SECONDS_"
      ;;
    start_session)
      # Begin a SECOND independent `opencode run` chain in the same RUN_CWD.
      run_start_session
      ;;
    session)
      # Pure focus switch — already handled by the inline target block above.
      :
      ;;
    *)
      echo "[driver] ERROR: unknown step type '$TYPE'" >&2
      EXIT_REASON="nonzero(2)"
      break
      ;;
  esac
done

echo "$EXIT_REASON" > "$STAGING/driver.exit-reason"

# Persist the final active slot's id before enumerating slots.
SES_IDS[$ACTIVE]="$SESSION_ID"

# Export ONE session's parts as a JSONL stream with the synthetic `_role`,
# `_cwd`, `_ts`, `_model`, `_error` fields the OpenCode parser expects. This is
# what the replay tool reads from transcript.jsonl in the committed fixture.
#   $1 = ses_ id   $2 = output path
# Role lives inside message.data JSON (no top-level column), so extract it with
# json_extract. modelID lives in message.data.model.modelID. `_error` carries
# message.data.error: an aborted/errored turn (quota, context-overflow,
# provider error) records the failure on the MESSAGE, not as a step-finish
# reason=error part — opencode emits only a bare step-start part on that
# message — so message.data.error is the sole turn-ending signal. The LIVE
# daemon settles such a turn via watcher.go isErrorMessage (reading the message
# row directly; #493); the REPLAY path has no SQLite, so it relies on this
# exported `_error` key — opencode/parser.go ParseLine maps a part carrying a
# non-null `_error` to turn_done. Without this export the replayed turn would
# never settle working→ready. `json(...)` nests the error sub-object as real
# JSON (SQLite JSON null when absent — both readers treat null as "no error").
# (#493 daemon side; this export + the parser.go `_error` check are the
# matching replay side.)
# Concurrent reads against opencode's running DB are safe — opencode writes in
# WAL mode and sqlite3's default open mode tolerates a parallel writer. The
# -readonly flag fails on this DB because it disables the WAL fallback path;
# omit it. `.timeout 5000` waits out a transient SQLITE_BUSY instead of
# producing a truncated/empty export.
export_session() {
  local sid="$1" out="$2"
  : > "$out"
  [[ -n "$sid" ]] || return 0
  sqlite3 "$OPENCODE_DB" <<SQL >> "$out"
.timeout 5000
.mode list
.separator ""
SELECT json_set(
  p.data,
  '\$._role',  json_extract(m.data, '\$.role'),
  '\$._cwd',   s.directory,
  '\$._ts',    p.time_updated,
  '\$._model', json_extract(m.data, '\$.model.modelID'),
  '\$._error', json(json_extract(m.data, '\$.error'))
)
FROM part p
JOIN message m ON p.message_id = m.id
JOIN session s ON p.session_id = s.id
WHERE p.session_id = '$sid'
ORDER BY p.time_created ASC, p.id ASC;
SQL
}

# Export every slot. Slot 1 keeps the canonical filename + back-compat
# session.uuid/transcript.path; further slots get .N suffixed files. Emit the
# multi-session contract (session.uuids / transcript.paths) with EVERY slot's
# id so a start_session run's BOTH arcs are curated — single-slot runs write
# one line each, the same shape, so run-cell.sh's multi-session branch is a
# no-op below count 2 (identical to claudecode's contract).
: > "$STAGING/session.uuids"
: > "$STAGING/transcript.paths"
PRIMARY_OUT="$STAGING/opencode-transcript.jsonl"
for (( s = 1; s <= N_SLOTS; s++ )); do
  if [[ "$s" -eq 1 ]]; then
    TRANSCRIPT_OUT="$PRIMARY_OUT"
  else
    TRANSCRIPT_OUT="$STAGING/opencode-transcript.$s.jsonl"
  fi
  export_session "${SES_IDS[$s]}" "$TRANSCRIPT_OUT"
  # Only emit a multi-session contract line for a slot that actually minted a
  # session. An allocated-but-unsent slot (start_session with no following send,
  # or a send that failed before capturing its id) leaves SES_IDS[$s] empty;
  # writing a blank uuid + empty transcript would desync session.uuids (which
  # run-cell.sh counts with `grep -c .`, ignoring the blank line) from
  # transcript.paths and make curation see a phantom empty arc.
  if [[ -n "${SES_IDS[$s]}" ]]; then
    echo "${SES_IDS[$s]}" >> "$STAGING/session.uuids"
    echo "$TRANSCRIPT_OUT" >> "$STAGING/transcript.paths"
  fi
  echo "[driver] slot #$s: ${SES_IDS[$s]:-<none>} -> $TRANSCRIPT_OUT ($(wc -l <"$TRANSCRIPT_OUT" | tr -d ' ') parts)" >&2
done

# Back-compat single-session pointers = slot 1.
echo "${SES_IDS[1]:-}" > "$STAGING/session.uuid"
echo "$PRIMARY_OUT" > "$STAGING/transcript.path"

# Combined log for easier review.
{
  echo "=== stdout ==="
  cat "$DRIVER_LOG.stdout" 2>/dev/null || true
  echo
  echo "=== stderr ==="
  cat "$DRIVER_LOG.stderr" 2>/dev/null || true
  echo
  echo "=== driver exit reason: $EXIT_REASON ==="
  echo "=== slots: $N_SLOTS (${SES_IDS[*]:1}) ==="
  echo "=== primary session_id: ${SES_IDS[1]:-<none>} ==="
} > "$DRIVER_LOG"

echo "drive-opencode-interactive: $EXIT_REASON (slots=$N_SLOTS, primary=${SES_IDS[1]:-<none>}, transcript=$PRIMARY_OUT)"

case "$EXIT_REASON" in
  ok) exit 0 ;;
  *)  exit 1 ;;
esac
