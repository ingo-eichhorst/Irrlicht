#!/usr/bin/env bash
# drive-muse-interactive.sh — drive muse's REPL via tmux, executing a
# step-script. SCAFFOLDED from scripts/templates/drive-interactive.sh.tmpl
# (#496 RC2): a new adapter starts with EVERY standard step-type arm present
# (stubbed), not a 3-step stub — so the column driver-gap forecast tells you
# which primitives still need porting, and the matrix can't silently freeze a
# cell on a missing arm.
#
# It also starts ON the shared _lib/drive slot model (slots.sh + contracts.sh):
# the multi-session bookkeeping and the staging-contract emission are already
# wired, so porting a multi-session step is "fill the seam + call
# alloc_slot/load_slot", never a mid-run refactor onto the slot model (#666).
#
# HOW TO USE THIS TEMPLATE
#   cp scripts/templates/drive-interactive.sh.tmpl \
#      scripts/drive-<agent>-interactive.sh
#   sed -i '' 's/muse/<agent>/g' scripts/drive-<agent>-interactive.sh
#   chmod +x scripts/drive-<agent>-interactive.sh
# Then fill the three AGENT-SPECIFIC SEAMS marked TODO(muse) below by
# porting from the reference drivers. For the slot-model multi-session arms
# (restart / resume / reset_session / start_session / session) read a slot-model
# driver — drive-codex-interactive.sh or drive-gemini-cli-interactive.sh;
# claudecode uses a different slot scheme and does NOT source _lib/drive.
#
# IMPORTANT — how a stubbed arm is caught: every standard arm is PRESENT here
# (so recipe-lint's GRAMMAR check treats it as handled and will NOT report a
# driver_gap for it). The real backstop is the SEMANTIC lint: the DRIVE_ELICITS
# constant below lists ONLY the step types this driver actually elicits, and
# recipe-lint reads it straight from this file (#508 #4 — no separate manifest)
# and flags any recipe needing a stubbed-but-unlisted primitive as a
# semantic_gap (exit 4) BEFORE recording. Keep DRIVE_ELICITS accurate: add a
# primitive the moment you genuinely port its seam, not when you stub the arm.
#
# Standard step types (port each from the reference driver):
#   send / slash   — type text + Enter (slash is the same keystrokes)
#   wait_turn      — block until the agent finishes the turn (SEAM 2)
#   interrupt      — cancel the in-flight turn (Escape / Ctrl-C)
#   keys           — raw tmux key sequence (arrow-key pickers, etc.)
#   sleep          — pause N seconds
#   reset_session  — in-REPL reset (/clear, /new): same process, new session id
#   restart        — end the session, start a FRESH one (new id, new cwd)
#   resume         — relaunch the SAME id+cwd (daemon sees one session, 2 PIDs)
#   sigkill        — kill -9 the active session's PID
#   exit_clean     — Ctrl-D graceful shutdown
#   start_session  — launch a concurrent session without tearing the first down
#   session        — switch the active slot (carried as {"session": N})
#
# ----------------------------------------------------------------------------
# HEADLESS ESCAPE HATCH
#   If muse has a true headless-per-turn mode (e.g. `muse run -p …`
#   that blocks until the turn ends), a tmux-REPL driver may be overkill for
#   the happy path — model the headless path like drive-opencode-interactive.sh
#   instead, where `send` launches a subprocess and `wait_turn` is a no-op. BUT:
#   headless modes usually CANNOT deliver in-REPL slash commands or signals
#   (opencode stores `/new` as literal text), so reset_session/slash/interrupt
#   still need a live-TUI path. opencode's driver carries BOTH: a headless path
#   and a run_live() tmux path the dispatcher picks when a recipe needs a TUI
#   primitive. Copy that hybrid shape if muse is headless-first.
# ----------------------------------------------------------------------------
#
# Staging contract (identical across all drivers — do NOT change these names):
#   driver.log[.stdout|.stderr]  — captured CLI output
#   driver.exit-reason           — ok | timeout | killed | nonzero(N)
#   session.uuid / session.uuids — the session id(s) the daemon will key on
#   transcript.path / transcript.paths — absolute path(s) to the transcript(s)
#
# Usage:
#   drive-muse-interactive.sh <staging-dir> <session-uuid> \
#       <timeout-seconds> <settings-path> <script-json>

set -euo pipefail

if [[ $# -ne 5 ]]; then
  echo "usage: drive-muse-interactive.sh <staging> <uuid> <timeout-s> <settings-path> <script-json>" >&2
  exit 2
fi

STAGING="$1"
# shellcheck disable=SC2034  # positional arg $2 of the driver protocol (tools/onboarding-factory/scripts/run-cell.sh:379); read by the sourced replaydata/_lib/drive/slots.sh (save_active/load_slot)
UUID="$2"            # preferred session id; some agents mint their own (ignore then)
TIMEOUT_S="$3"
# shellcheck disable=SC2034  # positional arg $4 of the driver protocol (tools/onboarding-factory/scripts/run-cell.sh:379); named so the slot is not silently reused, though this adapter's launch does not read it
SETTINGS_PATH="$4"   # scenario settings blob; wire into the launch if the agent reads one
SCRIPT_JSON="$5"

mkdir -p "$STAGING"
DRIVER_LOG="$STAGING/driver.log"

# MODEL pins `--model <m>` at launch when the cell's settings name one
# (ported from kiro-cli's driver-interactive.sh:107-112 — the same
# settings.model -> launch-argument pattern). `muse --help` documents a
# top-level `--model <MODEL>` flag ("Model id for non-echo providers");
# without this, `/model` opens an arrow-key picker that needs the still-
# unimplemented `keys` step, and a bare `slash`/`send` of "/model <id>" is
# live-confirmed (5.2 assessment) to open the SAME picker and silently
# ignore the trailing argument. Without a pin, model-identification (5.2)
# would silently launch the account default instead of a genuine
# non-default model.
MODEL=""
if [[ -f "$SETTINGS_PATH" ]]; then
  MODEL="$(jq -r '.model // empty' "$SETTINGS_PATH" 2>/dev/null)"
fi

# BASE_URL overrides the Meta provider endpoint at launch when the cell's
# settings name one (`muse --help`: "--base-url <URL> Override the Meta
# provider base URL"). turn-aborted-by-error (2.14) points this at a dead
# local port so every model call fails deterministically and with zero API
# spend, mirroring copilot's COPILOT_PROVIDER_BASE_URL-at-a-dead-port
# precedent for the same scenario. Without this, launch_repl/spawn_muse_repl
# ignored $SETTINGS_PATH entirely and the --base-url flag (confirmed present
# in `muse --help`) had nowhere to land.
BASE_URL=""
if [[ -f "$SETTINGS_PATH" ]]; then
  BASE_URL="$(jq -r '.base_url // empty' "$SETTINGS_PATH" 2>/dev/null)"
fi

# APPROVAL_JUDGE pins `--approval-judge <off|on>` at launch when the cell's
# settings name one — the third settings-driven launch arg, mechanically
# identical to MODEL/BASE_URL above (`muse --help`: "--approval-judge
# <off|on> LLM approval judge for Prompt-bound calls (default: on)").
# LOAD-BEARING for 2.19/2.26 (tool-gate-permission-prompt,
# permission-gate-non-mutating-tool): under the default judge-ON profile the
# SAME recipe still enters `waiting`, but an LLM judge resolves it in ~8-30s
# (decision_source.kind:"llm_judge") — a different, already-well-represented
# flavor of the scenario, not the genuinely human-decided one those cells
# need. Without this wire, $SETTINGS_PATH's `approval_judge` key had nowhere
# to land and every muse launch stayed on the judge-ON default regardless of
# what a recipe asked for.
APPROVAL_JUDGE=""
if [[ -f "$SETTINGS_PATH" ]]; then
  APPROVAL_JUDGE="$(jq -r '.approval_judge // empty' "$SETTINGS_PATH" 2>/dev/null)"
fi

# META_API_KEY overrides the credential muse launches with, injected as an
# ENVIRONMENT VARIABLE (not a CLI flag — `muse --help`/`muse exec --help`
# scanned in full, no `--api-key` flag exists anywhere) when the cell's
# settings name one (#1960 auth-credentials-rejected). muse's own binary
# strings (`AccountStateKind` enum loggedOut|envKey|apiKey|accountLogin, and
# the literal "META_API_KEY always takes priority over the account login.")
# confirm the env var is a genuine separate credential LANE that wins over
# whatever the real account's login state is — this is what lets a recipe
# supply a deliberately-invalid throwaway value without ever touching the
# real account's own credential. spawn_muse_repl wraps the launch in `env
# META_API_KEY=...` only when this is non-empty; every other launch (the
# overwhelming majority of cells) is byte-identical to before this wire.
#
# SAFETY, LIVE-VERIFIED BOTH DIRECTIONS (#1960 driver port): the assessment's
# own probe confirmed a bogus META_API_KEY never touches ~/.config/muse/
# auth.json (size/mtime unchanged). A SEPARATE live probe for this same port
# tried the opposite route — pointing --base-url at a local mock returning
# HTTP 401 while leaving the real account login active (no META_API_KEY set)
# — and that DID rewrite auth.json: muse's client attempted a real OAuth
# token refresh against Meta's real endpoint on the 401 and persisted the
# refreshed token to disk (mtime moved, ~26s after the probe). That is why
# this cell's recipe must always set meta_api_key rather than relying on
# --base-url alone: with the env-key lane active, muse authenticates via the
# env var directly and never enters the account-login refresh path at all.
META_API_KEY=""
if [[ -f "$SETTINGS_PATH" ]]; then
  META_API_KEY="$(jq -r '.meta_api_key // empty' "$SETTINGS_PATH" 2>/dev/null)"
fi

# Shared multi-session slot bookkeeping + staging-contract emission (#508 #3).
# The scaffolded driver lives at replaydata/agents/<agent>/driver-interactive.sh,
# so the lib is two dirs up under replaydata/_lib/drive. Sourcing it means a new
# column starts ON the slot model: porting a multi-session step is "wire the seam
# + call alloc_slot/load_slot", not rebuilding the slot bookkeeping (#666).
_DRIVE_LIB="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../_lib/drive" && pwd)"
# shellcheck disable=SC2034  # read by the sourced replaydata/_lib/drive/slots.sh:56 (alloc_slot)
DRIVE_MARKER_PREFIX="$STAGING/.muse-marker"
# shellcheck source=/dev/null
source "$_DRIVE_LIB/slots.sh"
# shellcheck source=/dev/null
source "$_DRIVE_LIB/contracts.sh"
# shellcheck source=/dev/null
source "$_DRIVE_LIB/teardown.sh"

# Slot state the lib reads/writes (the driver owns these globals). A run starts
# with zero slots; launch_repl allocs slot 1, and restart/start_session alloc
# more. ACTIVE indexes the live slot; SESSION/TRANSCRIPT/UUID mirror it.
N_SLOTS=0; ACTIVE=0
# Active-session view (pi pattern): step functions read/write these, kept in
# sync via save_active/load_slot. TRANSCRIPT is the absolute session.jsonl
# path; UUID is muse's own session id (the session dir basename == stream.id,
# NOT the preferred $2 UUID — muse mints its own, like pi).
TRANSCRIPT=""
UUID=""
EXPECTED_TURNS=0
SES_SESSION=()
SES_TRANSCRIPT=()
# shellcheck disable=SC2034  # driver-owned slot array; the sourced replaydata/_lib/drive/slots.sh reads it (save_active/load_slot/alloc_slot)
SES_UUID=()
# shellcheck disable=SC2034  # driver-owned slot array; the sourced replaydata/_lib/drive/slots.sh reads it (save_active/load_slot/alloc_slot)
SES_EXPECTED=()
# shellcheck disable=SC2034  # driver-owned slot array; the sourced replaydata/_lib/drive/slots.sh reads it (save_active/load_slot/alloc_slot)
SES_MARKER=()
SES_CWD=()
# SES_OWNED[i]=1 while this driver still owns (has not retired) the slot's
# tmux session — read by step_exit_clean's already-retired guard, same as
# kiro-cli's. Named for OWNERSHIP, not liveness (#1828): nothing re-derives it
# from `tmux has-session`, so the cleanup trap's teardown still gates on
# session-name presence and never on this.
SES_OWNED=()

# recipe-lint contract (#508 #4): the step types this driver genuinely ELICITS,
# read directly by recipe-lint (no separate manifest). Start with ONLY the seams
# that actually work in this scaffold (send/slash/sleep) and add each primitive
# as you port its seam — a stubbed `not_implemented` arm must NOT be listed, so
# recipe-lint flags a recipe needing it as a semantic_gap before recording. Set
# DRIVE_SLASH_REQUIRES_STEP_TYPE=true if muse is headless-first (a bare
# send "/cmd" stores literal text instead of reaching the REPL).
# shellcheck disable=SC2034  # scraped from this file's SOURCE by tools/onboarding-factory/scripts/lib/recipe-lint.sh:97 (sed), never expanded in shell
DRIVE_ELICITS="send slash wait_turn sleep exit_clean restart sigkill start_session session keys reset_session resume interrupt seed_instruction"
# shellcheck disable=SC2034  # scraped from this file's SOURCE by tools/onboarding-factory/scripts/lib/recipe-lint.sh:113 (sed), never expanded in shell
DRIVE_SLASH_REQUIRES_STEP_TYPE=false
RUN_CWD="${IRRLICHT_ONBOARD_CWD:-$STAGING/cwd}"
mkdir -p "$RUN_CWD"
RUN_CWD="$(cd "$RUN_CWD" && pwd -P)"   # canonicalize (resolve symlinks) for the daemon's cwd match
DEADLINE=$(( $(date +%s) + TIMEOUT_S ))
EXIT_REASON="ok"
# Raised to 1 by the epilogue, immediately before the final exit. cleanup() reads
# it to tell a run that FINISHED (EXIT_REASON is its verdict) apart from a `set
# -e` abort that never formed one (#1825) — see cleanup().
REACHED_EPILOGUE=0
SESSION=""

remaining_seconds() { local now; now=$(date +%s); (( now >= DEADLINE )) && echo 0 || echo $((DEADLINE - now)); }

not_implemented() { # <step-type>
  echo "[driver] STUB: step type '$1' not yet ported for muse — see scripts/templates/drive-interactive.sh.tmpl and drive-claudecode-interactive.sh" >&2
  EXIT_REASON="nonzero(3)"
  return 3
}

# Always honor the staging contract: write driver.exit-reason on ANY exit
# (including a `set -e` abort mid-launch) and tear tmux down if a session was
# started. Set EXIT_REASON before a failing `exit` so the reason is accurate.
#
# THE REACHED_EPILOGUE GUARD IS NOT OPTIONAL, and it is the half that the
# "set EXIT_REASON before a failing `exit`" sentence above does NOT cover
# (#1825). That discipline is per-`exit`: it works for the exits this file
# writes, and says nothing about a `set -e` abort, which is every other command
# in the driver. A driver with no trap at all wrote NO driver.exit-reason on an
# abort and run-cell.sh:418 read it as "unknown" — accurate, if unhelpful. A
# trap that writes "$EXIT_REASON" unconditionally turns that "unknown" into
# `ok`: the INITIAL value, reported as a verdict by a run that never formed one.
# That is the same "reports success while it actually failed" shape #1825 was
# opened for, reintroduced by the fix for it. So an abort that never reached the
# epilogue, with EXIT_REASON still at its initial `ok`, is recorded as a driver
# fault instead. A verdict already formed (timeout, readiness_timeout, …) is
# left exactly as the step that formed it set it.
#
# The other half of the contract is the epilogue raising REACHED_EPILOGUE=1
# immediately before its final exit. Port BOTH halves or neither: the flag
# without the guard does nothing, and the guard without the flag reports every
# successful run as a driver fault.
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

# --- AGENT-SPECIFIC SEAM 1: launch the REPL under tmux -----------------------
# `muse` with no subcommand starts the interactive TUI (`muse --help`: "If no
# subcommand is given, options run the interactive TUI"). Launched detached in
# $RUN_CWD, stdout/stderr to "$DRIVER_LOG.stdout|.stderr". muse mints its own
# session UUID, so the preferred $2 UUID is ignored and transcript resolution
# is deferred to step_wait_turn (SEAM 3, pi pattern) — the session dir may only
# materialize after the first prompt lands. NEVER pass --no-session-log: it
# disables the session.jsonl persistence the daemon tails.
# (`muse exec` is a true headless-per-turn mode; a future hybrid shape like
# drive-opencode-interactive.sh could use it, but the scaffold stays REPL-only.)
#
# --trust-workspace: every $RUN_CWD is a brand-new directory (`mkdir -p` under
# $STAGING/cwd), so without this muse's "Do you trust this workspace?" modal
# fires on EVERY run and silently eats the first prompt — live-reproduced
# (#1960 driver audit): sending "hello" against a bare `muse` launch produced
# zero run/terminal records; the keystrokes were consumed by the modal's
# picker instead (Enter accepted its pre-selected "Trust and continue", the
# letters did nothing). `--trust-workspace` ("Trust this workspace for this
# run... does not save trust", per `muse --help`) is the same class of fix
# mistral-vibe's `--trust` and kiro-cli's `--trust-all-tools` already carry.
# Live-verified fix: launching with this flag, the composer (`❯`) is ready
# immediately with no modal, and a `send` submits normally. Deliberately NOT
# `--yolo`, which also disables approval/sandboxing — out of scope for just
# skipping a dialog.
launch_repl() {
  command -v tmux >/dev/null 2>&1 || { echo "[driver] tmux required" >&2; EXIT_REASON="nonzero(2)"; exit 1; }
  # alloc_slot mints a fresh slot, points SESSION at its tmux name and ACTIVE at
  # it, and clears the slot's TRANSCRIPT/UUID. restart/start_session call it again
  # to open another session; per-slot stdout (.stdout.$ACTIVE) feeds the contract.
  alloc_slot "musedrv-$$-$(date +%s)-$((N_SLOTS + 1))" "$RUN_CWD"
  spawn_muse_repl
}

# spawn_muse_repl launches `muse` in tmux for the CURRENTLY ACTIVE slot (caller
# already ran alloc_slot, or — for step_resume, #1960 SEAM 3 — repointed
# $SESSION at a rotated tmux name on the SAME slot). Extracted from
# launch_repl (#1960 stage 3) so step_restart reuses the identical launch
# shape — same MUSE_ARGS construction (settings_model's --model pin, and any
# future launch-arg wiring like base_url_override), same settle-delay
# rationale — instead of drifting a second, hand-copied tmux new-session call
# out of sync with the first.
#
# Optional leading argv ($@, e.g. `resume <uuid>`) is inserted BEFORE
# MUSE_ARGS: `muse --help` documents root options (--trust-workspace,
# --model, …) as valid on either side of a subcommand, and every launch site
# this driver has (bare TUI, `resume <uuid>`) puts the subcommand first —
# `muse resume <uuid> --trust-workspace`, not `muse --trust-workspace resume
# <uuid>` — so callers pass the subcommand tokens as positional args rather
# than folding them into MUSE_ARGS.
spawn_muse_repl() { # [subcommand-args...]
  tmux kill-session -t "$SESSION" 2>/dev/null || true
  # MUSE_ARGS: the flags every launch of muse carries beyond --trust-workspace
  # and -c <cwd> (fixed per tmux new-session call below). Built as an array,
  # not string-concatenated, so a value containing whitespace stays one argv
  # entry when it reaches tmux new-session's trailing-argv passthrough.
  MUSE_ARGS=(--trust-workspace)
  [[ -n "$MODEL" ]] && MUSE_ARGS+=(--model "$MODEL")
  [[ -n "$BASE_URL" ]] && MUSE_ARGS+=(--base-url "$BASE_URL")
  [[ -n "$APPROVAL_JUDGE" ]] && MUSE_ARGS+=(--approval-judge "$APPROVAL_JUDGE")
  # LAUNCH_CMD: the executable (+ optional `env VAR=value` wrapper) muse
  # itself runs under. Always at least one element ("muse") so this is safe
  # to expand under this file's `set -u` on bash 3.2 (the stock macOS
  # /bin/bash) — an empty array's "${arr[@]}" is a hard "unbound variable"
  # error on that bash, unlike modern bash/zsh, so this is built as a
  # never-empty array rather than a separately-expanded optional prefix.
  LAUNCH_CMD=(muse)
  [[ -n "$META_API_KEY" ]] && LAUNCH_CMD=(env "META_API_KEY=$META_API_KEY" muse)
  # `|| { … exit … }` keeps a launch failure from aborting under set -e WITHOUT
  # an accurate exit-reason — the cleanup trap then records nonzero(2).
  tmux new-session -d -s "$SESSION" -x 200 -y 50 -c "${SES_CWD[$ACTIVE]}" "${LAUNCH_CMD[@]}" "$@" "${MUSE_ARGS[@]}" \
    >>"$DRIVER_LOG.stdout.$ACTIVE" 2>>"$DRIVER_LOG.stderr" \
    || { echo "[driver] failed to launch muse under tmux" >&2; EXIT_REASON="nonzero(2)"; exit 1; }
  # Startup settle delay — a TUI input-timing requirement, not a wait for an
  # observable condition (same class as step_send's 0.3s, not a candidate for
  # a poll): live-reproduced (#1960 driver audit follow-up) that the FIRST
  # step_send after `launch_repl` returns is dropped just like a bare Enter
  # is — but this is a DIFFERENT race than the trust-modal one --trust-workspace
  # fixes above. `tmux new-session -d` returns as soon as the pane is forked,
  # not once muse's own input handler is wired up, and a driver's step_send
  # fires within milliseconds of that return with zero gap — 3/3 full
  # end-to-end driver runs (60s/90s/120s timeouts) lost the first prompt this
  # way and timed out with zero turns. The rendered pane is NOT a reliable
  # readiness signal here (unlike copilot's footer-hint poll): capturing the
  # pane seconds into one of these losing runs already showed the fully
  # "ready-looking" banner + composer, because muse catches up and repaints
  # within a couple of seconds regardless of whether the earlier keystrokes
  # landed — so polling for that text would return "ready" on the very runs
  # that just lost the prompt. A flat delay before the first send is the
  # verified fix: 3/3 trials with zero delay dropped the prompt (no
  # run/terminal record ever appeared); 3/3 trials with this 1s delay
  # submitted immediately and got a real reply.
  sleep 1
}

# muse persists sessions under $MUSE_DATA_ROOT/sessions/<YYYY>/<MM>/<DD>/<uuid>/
# (default $HOME/.local/share/muse — observed on this machine; no data-root
# override flag exists in `muse --help`, only --no-session-log, which the
# driver must never pass). MUSE_DATA_ROOT is therefore a DRIVER-level override
# for transcript resolution only, and recordings land in the maintainer's live
# store until muse gains a relocatable root (cf. CODEX_HOME / HERMES_HOME).
MUSE_DATA_ROOT="${MUSE_DATA_ROOT:-$HOME/.local/share/muse}"

# --- AGENT-SPECIFIC SEAM 3: resolve the transcript muse just created ---------
# The session dir appears at/after launch; the session.jsonl inside grows with
# every event. Find the newest session.jsonl NEWER than this slot's $MARKER
# (pi pattern — a resume/restart bumps the marker and excludes the prior file).
# The session id is the parent dir basename (== stream.id on every observed
# session.jsonl); the file basename is always "session.jsonl", so daemon_sid
# of the transcript path is useless — the epilogue passes SES_UUID instead.
resolve_transcript() {
  if [[ -n "$TRANSCRIPT" ]]; then return 0; fi
  local start_s; start_s=$(date +%s)
  # Bounded by the run's real $DEADLINE, not a fixed local cap. The original
  # `for _ in $(seq 1 60)` (~30s at 0.5s/iter) was disconnected from
  # $TIMEOUT_S/$DEADLINE — flagged in the #1960 driver audit as a structural
  # risk read from the code (not reproduced live: every launch on this
  # machine materialized the session dir well under 1s). Fixed defensively
  # here so a short overall timeout can't keep polling past its real budget,
  # and a long timeout doesn't give up at a stale 30s with the deadline
  # nowhere close.
  while [[ $(date +%s) -lt $DEADLINE ]]; do
    local candidate=""
    # `-not -path '*/subagent/*'` excludes subagent transcripts (#1960 driver
    # audit, live-reproduced): muse spawns goal-reminder/verify-reminder
    # subagents even for a single simple prompt, materializing
    # subagent/<child>/session.jsonl under the SAME session directory. Without
    # this exclusion, `find | sort | tail -n1` recurses into subagent/ and
    # sorts LEXICALLY by full pathname — "session.jsonl" < "subagent/…" as
    # strings, so once a subagent transcript exists it always wins `tail -n1`
    # over the driven session's own transcript sitting right next to it.
    # Live-reproduced on a real driven session with two reminder subagents:
    # the buggy find (no exclusion) resolved to
    # .../subagent/<child>/session.jsonl; this exclusion resolves to the
    # session's own .../session.jsonl, scoped to that one session's family.
    candidate="$(find "$MUSE_DATA_ROOT/sessions" -type f -name 'session.jsonl' \
                  -not -path '*/subagent/*' -newer "$MARKER" 2>/dev/null | sort | tail -n1)"
    if [[ -n "$candidate" && -s "$candidate" ]]; then
      # Sanity: first line must be a JSON record with this session's stream id.
      local sid=""
      sid="$(basename "$(dirname "$candidate")")"
      if head -n1 "$candidate" | jq -e . >/dev/null 2>&1; then
        TRANSCRIPT="$candidate"
        UUID="$sid"
        SES_TRANSCRIPT[$ACTIVE]="$TRANSCRIPT"
        SES_UUID[$ACTIVE]="$UUID"
        echo "[driver] resolve_transcript[s$ACTIVE]: $TRANSCRIPT (uuid=$UUID)" >&2
        return 0
      fi
    fi
    sleep 0.5
  done
  echo "[driver] resolve_transcript[s$ACTIVE]: FAILED after $(( $(date +%s) - start_s ))s" \
       "— no session.jsonl (excluding subagent/) appeared under" \
       "$MUSE_DATA_ROOT/sessions newer than $MARKER" >&2
  return 1
}

# --- AGENT-SPECIFIC SEAM 2: count completed turns -----------------------------
# Extracted to a sibling turn-count.sh (matches every other adapter) so it can
# be unit-tested without executing the driver, which walks its recipe at
# source time (adapter-tables_test.sh B4 — an inline turn_count is invisible
# to replaydata/_lib/drive/turn-count_test.sh: nothing fails, it is simply not
# covered). See that file's header for the canonical-shape rationale and the
# fail-loud fix (#1960 driver audit defect #5).
# shellcheck source=./turn-count.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/turn-count.sh"

# Track expected vs. actual completed turns (pi pattern): each `send` bumps
# EXPECTED_TURNS by 1; wait_turn waits for actual >= expected. Without this a
# fast turn can complete before resolve_transcript returns and a naive
# before/after snapshot waits forever. Slash commands bill a turn the same as
# sends until record proves muse handles a no-LLM slash locally (cf. 2.5).
step_send() { # <text>
  local text="$1"
  tmux send-keys -t "$SESSION" -l -- "$text"
  # This is a TUI input-timing requirement, NOT a wait for an observable
  # condition (contrast with the deadline-bounded polls elsewhere in this
  # file): live-reproduced twice (#1960 driver audit) that muse's TUI drops a
  # bare Enter sent back-to-back with the text — the input handler is still
  # rendering the typed text when Enter arrives, so it lands mid-render and
  # never submits (2/2 failed with no delay; 1/1 submitted immediately with
  # this sleep). Ported verbatim from mistral-vibe/kiro-cli/copilot, which
  # each independently discovered the identical race for their own TUIs.
  sleep 0.3
  tmux send-keys -t "$SESSION" Enter
  EXPECTED_TURNS=$((EXPECTED_TURNS + 1))
  echo "[driver] send[s$ACTIVE]: ${text:0:60} (expecting turn $EXPECTED_TURNS)" >&2
}

step_wait_turn() {
  resolve_transcript || {
    echo "[driver] wait_turn[s$ACTIVE]: muse never created a transcript under $MUSE_DATA_ROOT/sessions" >&2
    EXIT_REASON="readiness_timeout"
    return 1
  }
  local now=0 rc=0 stale_reads=0 total_reads=0
  while [[ $(date +%s) -lt $DEADLINE ]]; do
    rc=0
    now=$(turn_count) || rc=$?
    total_reads=$((total_reads + 1))
    if [[ $rc -ne 0 ]]; then
      # turn_count's return code (not a global — see turn-count.sh's header
      # for why) says jq could not read $TRANSCRIPT at all this poll, as
      # opposed to genuinely finding zero completed turns (#1960 driver audit
      # defect #5's fail-loud fix). Logged immediately rather than only
      # inferred from the eventual timeout message below.
      stale_reads=$((stale_reads + 1))
      echo "[driver] wait_turn[s$ACTIVE]: turn_count could not read $TRANSCRIPT this poll (jq failed, zero matches) — not the same as zero turns" >&2
    fi
    if [[ $now -ge $EXPECTED_TURNS ]]; then
      echo "[driver] wait_turn[s$ACTIVE]: count=$now (expected ≥ $EXPECTED_TURNS)" >&2
      return 0
    fi
    sleep 1
  done
  if [[ $total_reads -gt 0 && $stale_reads -eq $total_reads ]]; then
    # Every single poll failed to read the transcript — this is a read
    # failure, not a slow model, and must not report as an ordinary timeout
    # (the exact ambiguity #1960's audit flagged: "step_wait_turn just spins
    # to $DEADLINE and reports a generic timeout ... indistinguishable from
    # the model being slow").
    echo "[driver] wait_turn[s$ACTIVE]: FAILED TO READ — every poll of $TRANSCRIPT errored with zero matches ($stale_reads/$total_reads polls)" >&2
    EXIT_REASON="nonzero(4)"
  else
    echo "[driver] wait_turn[s$ACTIVE]: timeout (count=$now, expected ≥ $EXPECTED_TURNS)" >&2
    EXIT_REASON="timeout"
  fi
  return 1
}

wait_turn() {
  step_wait_turn
}

# step_interrupt cancels the in-flight turn (#1960 driver port). Ported from
# claudecode's step_interrupt (driver-interactive.sh:469-477): `tmux send-keys
# -t "$SESSION" Escape` is the exact primitive the 2-20 assessment
# live-confirmed against muse 1.2.1's TUI (probe: Escape sent mid-"esc to
# interrupt" produced an immediate "cancelled" run terminal on disk), retargeted
# from claudecode's $CURRENT_TMUX onto this driver's own active-slot mirror var
# $SESSION (matching step_send/step_keys's existing idiom).
#
# DELIBERATELY DOES NOT decrement EXPECTED_TURNS, unlike claudecode's port
# target. claudecode's turn_count() (turn-count.sh) counts ONLY
# `stop_reason=="end_turn"` records, so an interrupted turn (stop_reason=
# "stop_sequence") is invisible to it and the counter must be walked back or
# a later wait_turn spins to timeout waiting for a completion that can never
# land. muse's turn_count() (turn-count.sh) counts ANY ("run","terminal")
# record regardless of its terminal value — verified by reading the jq
# predicate, which matches on `event.kind=="terminal"` alone, with no
# `terminal=="completed"` filter — so an interrupted run's own
# terminal:"cancelled" record DOES increment the count, the same as a normal
# completion would. Decrementing here would desync the two: EXPECTED_TURNS
# would drop to 0, then the next `send` would bump it back to 1, and the
# following `wait_turn` would return immediately on the ALREADY-present
# cancelled-turn count instead of waiting for that next turn's own
# completion — silently truncating the very recording this primitive exists
# to capture. Leaving EXPECTED_TURNS untouched keeps it counting one unit per
# `send` regardless of how that turn ends, which is exactly what muse's
# terminal-value-agnostic turn_count() also does.
step_interrupt() {
  tmux send-keys -t "$SESSION" Escape
  echo "[driver] interrupt[s$ACTIVE] (Escape, EXPECTED_TURNS left at $EXPECTED_TURNS — muse's turn_count() counts a cancelled terminal too)" >&2
  sleep 1
}

# step_keys sends a raw tmux key sequence (NOT literal text) to the active
# slot's pane — arrow-key pickers (e.g. /model's picker, 5.3), a bare
# non-Enter keypress that a menu applies on keydown (e.g. the approval
# dialog's "1", 2.19/2.26), Escape, etc. Ported from claudecode's
# step_keys/codex's inline `keys)` arm (both five lines, both send raw and
# both do NOT append an Enter — unlike step_send, the caller decides exactly
# which keys go, including whether Enter is one of them), re-targeted from
# claudecode's $CURRENT_TMUX onto muse's own active-slot mirror var $SESSION
# (slots.sh) to match this driver's existing idiom (step_send, spawn_muse_repl
# all address $SESSION, never a second tmux-target variable).
#
# Deliberately does NOT bump EXPECTED_TURNS. turn_count() (turn-count.sh)
# only counts a completed ("run","terminal") record — a genuine LLM turn —
# and a raw key sequence (menu navigation, a keydown-applied approval choice)
# never produces one of those on its own; the model reconfigure/approval
# bookkeeping it triggers is Skip=true in the daemon parser (5.3's own
# assessment). Live-confirmed this matters, not just symmetry with the
# reference drivers: 5.3's assessment independently hit the SAME failure this
# port must not reintroduce — submitting "/model" via `slash` (=step_send)
# already bumps EXPECTED_TURNS even though /model never completes a matching
# turn, so its recipe deliberately awaits turn 2 with a bounded `sleep`
# instead of `wait_turn` to avoid blocking on a count that can never be
# reached. Had `keys` also bumped the counter (e.g. for the Down/Enter picker
# steps), the counter would run further ahead of what any `wait_turn` in a
# keys-using recipe could ever observe, guaranteeing a timeout on shapes that
# use `keys` inside a normal wait_turn-gated turn (2.19/2.26's approval
# keypress, sent mid-turn while the SAME send's `wait_turn` still needs to
# observe that turn's own eventual terminal record).
#
# Recipe step shape:
#   {"type": "keys", "keys": "1"}
#   {"type": "keys", "keys": "Down"}
#   {"type": "keys", "keys": "Down Down Enter"}
step_keys() { # <keys>
  local keys="$1"
  # shellcheck disable=SC2086  # intentional word-splitting of the key list
  tmux send-keys -t "$SESSION" $keys
  echo "[driver] keys[s$ACTIVE]: $keys" >&2
  sleep 0.3
}

# --- AGENT-SPECIFIC SEAM 4: graceful teardown ---------------------------------
# `/exit` ("Quit when idle", confirmed live via muse's own `/` command menu)
# is muse's clean-shutdown slash command. Wired up here in place of the
# `not_implemented` stub (#1960 driver audit defect #4): the cleanup trap's
# ONLY teardown path was always `tmux kill-session` — the SIGKILL/ghost-
# session disk shape, live-confirmed to leave zero session.end records before
# OR after the kill. `/exit` was live-verified instead to append a real
# session.end record with exit_reason:"clean" to session.jsonl. This function
# is what a recipe calls (exit_clean step) to get that clean-exit shape on
# disk; a recipe with no exit_clean step still falls through to the trap's
# tmux kill-session, same as every other adapter in this fleet (kiro-cli's
# step_exit_clean is the identical shape, /quit in place of /exit).
step_exit_clean() {
  if [[ "${SES_OWNED[$ACTIVE]:-0}" != "1" ]]; then
    echo "[driver] exit_clean[s$ACTIVE]: slot already retired -- refusing (its tmux/process may be owned by another live slot)" >&2
    return 0
  fi
  resolve_transcript || true
  tmux send-keys -t "$SESSION" -l -- "/exit"
  # Same TUI input-timing requirement as step_send (#1960 driver audit
  # defect #2) — not a wait for an observable condition.
  sleep 0.3
  tmux send-keys -t "$SESSION" Enter
  # STRICT poll (mirrors kiro-cli/copilot post-#1825): require_tmux_session_gone
  # only returns 0 when the session was actually OBSERVED gone, never on a
  # best-effort cap expiry — so an `/exit` that stopped working reads as a
  # real failure instead of a silent exit-reason=ok. Cap: DRIVE_EXIT_CLEAN_CAP_S
  # (_lib/drive/teardown.sh) — the fleet-uniform generous bound, not a
  # muse-specific measurement.
  if require_tmux_session_gone "$SESSION" "$DRIVE_EXIT_CLEAN_CAP_S"; then
    SES_OWNED[$ACTIVE]=0
    echo "[driver] exit_clean[s$ACTIVE]: sent /exit to $SESSION (uuid=$UUID, session gone)" >&2
  else
    echo "[driver] exit_clean[s$ACTIVE]: FAILED — $SESSION still alive ${DRIVE_EXIT_CLEAN_CAP_S}s after /exit;" \
         "killing it explicitly. muse did NOT shut down gracefully, so this" \
         "recording has no real clean-exit session.end." >&2
    tmux kill-session -t "$SESSION" 2>/dev/null || true
    SES_OWNED[$ACTIVE]=0
    EXIT_REASON="nonzero(2)"
  fi
}

# step_restart (ported from claudecode's step_restart, driver-interactive.sh:
# 497-516, adapted from claudecode's own CURRENT_TMUX/CURRENT_UUID slot scheme
# onto muse's shared _lib/drive/slots.sh model): end the active session's
# lifecycle and start a FRESH one in a NEW cwd. The old slot is preserved
# (save_active) so the epilogue still flushes its uuid + transcript; only its
# tmux is retired (SES_OWNED=0, kill-session) before alloc_slot opens the next
# one. A fresh cwd matters here the same way it does for claudecode: every
# muse launch already carries --trust-workspace (per-invocation, not
# persisted, muse-driver-audit.md), so reusing a cwd is not known to break
# anything, but a fresh directory is the already-proven-safe shape session-end's
# own assessment caveat calls for.
# step_start_session (ported from pi's step_start_session,
# driver-interactive.sh:475-497 — pi is the closest architectural sibling
# here: muse's driver already follows the "pi pattern" throughout, both
# source the same shared _lib/drive/slots.sh slot model, and both defer
# transcript discovery to wait_turn). Launches a NEW concurrent muse REPL
# WITHOUT tearing down the active session — the multiple-sessions-same-cwd
# scenario's whole point. Defaults to the SAME cwd as the caller's active
# slot (the scenario's default shape); pass {"type":"start_session","cwd":
# "…"} to launch elsewhere.
#
# resolve_transcript on the OUTGOING active slot BEFORE allocating the new
# one is load-bearing, not decorative: muse-driver-audit.md documents a
# live-reproduced hazard (item 3) where resolve_transcript's `find ...
# -newer $MARKER | sort | tail -n1` can resolve to a concurrent, unrelated
# session's transcript if called before the first is cached — pinning the
# outgoing slot's transcript here keeps its resolution outside that race
# window, mirroring pi's own comment on the identical seam.
step_start_session() {
  local req_cwd="$1"
  resolve_transcript || true
  save_active
  local idx=$(( N_SLOTS + 1 ))
  local new_cwd="${req_cwd:-$RUN_CWD}"
  alloc_slot "musedrv-$$-$(date +%s)-${idx}" "$new_cwd"
  mkdir -p "${SES_CWD[$ACTIVE]}"
  echo "[driver] start_session: concurrent session slot #${ACTIVE} (cwd=${SES_CWD[$ACTIVE]})" >&2
  spawn_muse_repl
}

step_restart() {
  save_active
  SES_OWNED[$ACTIVE]=0
  tmux kill-session -t "$SESSION" 2>/dev/null || true
  sleep 1
  local idx=$(( N_SLOTS + 1 ))
  alloc_slot "musedrv-$$-$(date +%s)-${idx}" "$STAGING/cwd-${idx}"
  mkdir -p "${SES_CWD[$ACTIVE]}"
  echo "[driver] restart: new session slot #${ACTIVE} (cwd=${SES_CWD[$ACTIVE]})" >&2
  spawn_muse_repl
}

# step_reset_session (#1960 SEAM 3 — ported from codex's swap_after_slash,
# driver-interactive.sh:658-683, NOT claudecode's step_reset_session: muse
# already sources the same shared _lib/drive/slots.sh slot model codex does
# — the template's own header names codex/gemini-cli as the reference for
# every slot-model multi-session arm — so codex's "retire the old slot,
# allocate a NEW one reusing the same tmux/process" shape ports directly,
# where claudecode's hand-rolled slug-dir rescan does not).
#
# Empirically confirmed BEFORE writing this (throwaway tmux dir, muse 1.2.1,
# 2026-09-15 — not inferred from the assessment's own probe): `/clear` mints
# a genuinely new session DIRECTORY (new UUIDv7) under the SAME muse-bin PID,
# and the old directory's session.jsonl gets a real session.end{exit_reason:
# clean} appended — never rewritten in place, never truncated. So unlike
# codex's `/new` (LAZY — new rollout materializes only on the next user
# message), muse's rotation is EAGER, the same shape as codex's own `/fork`:
# the new session.jsonl exists within ~1-2s of the keystroke, before this
# function even returns. That still doesn't need an inline poll here —
# resolve_transcript's marker-based rediscovery (deferred to the NEXT
# wait_turn, exactly as it is for a first launch) finds it regardless of
# exactly when it appeared, because the recipe's very next step is always
# another send + wait_turn, which forces at least one more write to the new
# file (refreshing its mtime well past the new slot's marker) before
# resolve_transcript's `find -newer $MARKER` is ever evaluated.
#
# $1 is the in-REPL command to send, default "/clear" (1-5_session-reset's
# recipe omits `text` entirely; 1-6_checkpoint-rewind's passes "/rewind").
# "/rewind" is a three-Enter flow (submit -> accept the picker's pre-selected
# most-recent candidate -> confirm the "Rewind conversation" dialog) that
# needs NO arrow-key navigation — live-confirmed by 1-6's own assessment
# probes (both cited in its metadata.json) and not re-litigated here; "/clear"
# and "/new" are a single submit with no follow-on picker, and sending two
# extra bare Enters into "/clear"'s freshly-emptied composer would submit a
# spurious blank user turn — so the extra Enters are gated strictly to
# "/rewind", never sent unconditionally.
step_reset_session() { # [slash-text, default "/clear"]
  local slash="${1:-/clear}"
  resolve_transcript || true
  local old_tmux="$SESSION"
  local old_cwd="${SES_CWD[$ACTIVE]}"
  save_active
  echo "[driver] reset_session ($slash): recorded old session uuid=$UUID" >&2
  # shellcheck disable=SC2034  # SES_OWNED write-only since #1825 (teardown gates on session-name PRESENCE, not this flag) — set for parity with restart/start_session's own retiring-slot convention
  SES_OWNED[$ACTIVE]=0

  tmux send-keys -t "$old_tmux" -l -- "$slash"
  sleep 0.3
  tmux send-keys -t "$old_tmux" Enter

  if [[ "$slash" == "/rewind" ]]; then
    sleep 0.5
    tmux send-keys -t "$old_tmux" Enter   # accept the picker's default (most-recent) candidate
    sleep 0.5
    tmux send-keys -t "$old_tmux" Enter   # confirm the "Rewind conversation" dialog's default option
    echo "[driver] reset_session ($slash): accepted picker default + confirm dialog" >&2
  fi
  echo "[driver] reset_session ($slash): sent to $old_tmux" >&2

  # Settle before minting the new slot's marker (same-second mtime hazard as
  # codex's swap_after_slash — sleep first so the marker sorts strictly after
  # whatever the OLD session's own /clear|/rewind bookkeeping just wrote).
  sleep 2
  alloc_slot "$old_tmux" "$old_cwd"
  SES_OWNED[$ACTIVE]=1
  echo "[driver] reset_session ($slash): new slot #${ACTIVE}, marker bumped, awaiting new session" >&2
  sleep 1
}

# step_resume (#1960 SEAM 3 — ported from codex's step_resume,
# driver-interactive.sh:713-746, same "already on the slot-model" reasoning
# as step_reset_session above). Empirically confirmed BEFORE writing this
# (throwaway tmux dir, muse 1.2.1, 2026-09-15): clean-exit then `muse resume
# <uuid>` in the SAME cwd REOPENS AND APPENDS TO THE SAME session.jsonl file
# under a NEW PID — the session directory never changes, the file only grows
# (70 -> 76 -> 132 lines across two prompts and the resume, in this probe).
# So this is the SAME slot with a new process lifetime, not a new one:
# TRANSCRIPT/UUID/MARKER/EXPECTED_TURNS are left exactly as they are — no
# alloc_slot, no resolve_transcript reset — because turn_count() (turn-
# count.sh) counts ("run","terminal") records cumulatively over the WHOLE
# file; resetting EXPECTED_TURNS here would make wait_turn's post-resume
# `now -ge $EXPECTED_TURNS` check pass on the file's FIRST (pre-exit) turn
# instead of waiting for the genuinely new one.
#
# Only $SESSION (the tmux target) rotates, mirroring codex's own step_resume
# comment ("Only the tmux session name rotates"). Relaunches via the SAME
# spawn_muse_repl every other launch site uses (`muse resume <uuid>` as
# leading argv, then the identical --trust-workspace/--model/--base-url/
# --approval-judge MUSE_ARGS as the original launch and the same 1s settle
# sleep) rather than hand-copying a second tmux new-session call out of sync
# with it — the precedent the fleet has already paid for once
# (spawn_muse_repl's own header comment) and should not pay for twice.
step_resume() {
  resolve_transcript || true
  if [[ -z "$UUID" ]]; then
    echo "[driver] resume[s$ACTIVE]: FAILED — no session uuid resolved yet for the active slot; nothing to resume" >&2
    EXIT_REASON="nonzero(2)"
    return 1
  fi
  local resume_uuid="$UUID" old_tmux="$SESSION"
  # Defensive, not the primary teardown: every recipe using `resume` sends it
  # after exit_clean (or sigkill), which already confirmed-or-force-killed
  # the old pane. Idempotent no-op on the expected path.
  tmux kill-session -t "$old_tmux" 2>/dev/null || true
  SESSION="musedrv-$$-$(date +%s)-r${ACTIVE}"
  echo "[driver] resume[s$ACTIVE]: relaunch muse resume $resume_uuid (same transcript=$TRANSCRIPT, new tmux=$SESSION)" >&2
  spawn_muse_repl resume "$resume_uuid"
}

# muse_writer_pid <path> -> the PID holding <path> open for WRITING (FD access
# mode 'w' or 'u'), or empty. Mirrors core/adapters/inbound/agents/
# processlifecycle/process_darwin.go's writerPIDFromLsof exactly: reads the
# FD column's access-mode letter (the first non-digit byte, tolerating a
# trailing lock character like "14uW") from lsof's default columnar table.
#
# THIS IS NOT COSMETIC. `lsof -t <path>` (terse mode) drops the FD column
# entirely and returns every PID with the file open in ANY mode, write or
# read — live-reproduced (#1960 session-end driver-port incident): the
# coexisting recording daemon holds a READ fd on a muse session's own
# .session.lock while tailing it (muse has no isolated-home knob, so the
# daemon watches the SAME real $HOME tree the CLI writes into), and a plain
# `lsof -t "$lockfile" | head -1` picked the DAEMON's PID over the real
# muse-bin writer's on the very first live attempt — sigkill then killed the
# recording daemon itself mid-run instead of the target session. Filtering
# to 'w'/'u' only, exactly as the daemon's own DiscoverPID does, is what
# distinguishes "the process holding this session's lock" (the subject) from
# "some other process that merely has the file open" (a side effect of
# having a watcher running at all).
muse_writer_pid() {
  # NEVER name this local "path" — zsh ties a special `path` array to $PATH
  # (case-insensitively), so `local path=...` inside a zsh-invoked function
  # silently clobbers $PATH for the rest of the call and every subsequent
  # subprocess lookup (awk included) starts failing with a bare "command not
  # found" that has nothing to do with awk itself (live-reproduced while
  # verifying this very function interactively). Bash has no such tie, so
  # this would never break the driver's own #!/usr/bin/env bash execution —
  # but it is exactly the kind of landmine that makes interactive
  # verification of a sigkill primitive lie to you, so it's named
  # target_path everywhere in this function on principle.
  local target_path="$1"
  # `c < "0" || c > "9"` rather than a `!~ /[0-9]/` regex negation — the
  # latter is byte-identical awk but a live `!~` landmine in an interactive
  # zsh (histexpand can swallow `!` mid-token); this form has no `!` at all.
  lsof -- "$target_path" 2>/dev/null | awk 'NR>1 {
    fd = $4
    mode = ""
    for (i = 1; i <= length(fd); i++) {
      c = substr(fd, i, 1)
      if (c < "0" || c > "9") { mode = c; break }
    }
    if (mode == "w" || mode == "u") { print $2; exit }
  }'
}

# step_sigkill (ported from claudecode's step_sigkill, driver-interactive.sh:
# 536-553, re-targeted at muse's OWN PID-discovery mechanism instead of a
# `pgrep -f --session-id` argv match, which has no muse equivalent — muse
# mints its own session id and never receives one on argv). Finds the real
# muse-bin PID the SAME way the daemon does (core/adapters/inbound/agents/
# muse/pid.go's DiscoverPID): the WRITER of the session's own .session.lock
# file (a sibling of session.jsonl, via muse_writer_pid above), falling back
# to the transcript file itself if the lock has no writer (pid.go's own
# fallback, for the same reason). This observes the SUBJECT (the actual
# process bound to this session, via the same signal irrlichd uses) rather
# than a side effect — NOT tmux's pane_pid, which would be a proxy for "the
# process tmux spawned" rather than "the process holding this session's
# lock", and NOT a bare `lsof -t` (see muse_writer_pid's comment for why that
# form is actively dangerous here, not just imprecise).
#
# A second, independent safety net on top of the mode filter: before killing
# anything, confirm the candidate PID's own command name actually names
# muse. This is deliberately redundant with the mode filter above -- belt
# and braces after a real incident where the wrong PID reached `kill -9` --
# and it fails loudly (EXIT_REASON, return 1) rather than ever killing an
# unverified PID. Likewise for "no writer found": claudecode's own
# step_sigkill only logs and continues on a miss, which is the "cannot look"
# case reading identically to "looked and found nothing" this porting
# explicitly avoids.
step_sigkill() {
  resolve_transcript || true
  local lockfile="" pid=""
  command -v lsof >/dev/null 2>&1 || { echo "[driver] sigkill[s$ACTIVE]: FAILED — lsof not on PATH, cannot discover the muse PID to kill" >&2; EXIT_REASON="nonzero(2)"; return 1; }
  if [[ -n "$TRANSCRIPT" ]]; then
    lockfile="$(dirname "$TRANSCRIPT")/.session.lock"
    pid="$(muse_writer_pid "$lockfile")"
    if [[ -z "$pid" ]]; then
      pid="$(muse_writer_pid "$TRANSCRIPT")"
    fi
  fi
  if [[ -z "$pid" ]]; then
    echo "[driver] sigkill[s$ACTIVE]: FAILED — no WRITER found for $lockfile or $TRANSCRIPT; nothing killed (uuid=$UUID)" >&2
    EXIT_REASON="nonzero(2)"
    return 1
  fi
  local comm=""
  comm="$(ps -p "$pid" -o comm= 2>/dev/null | tr '[:upper:]' '[:lower:]')"
  if [[ "$comm" != *muse* ]]; then
    echo "[driver] sigkill[s$ACTIVE]: FAILED — refusing to kill PID $pid: its command ('$comm') does not name muse (lock=$lockfile, uuid=$UUID)" >&2
    EXIT_REASON="nonzero(2)"
    return 1
  fi
  kill -9 "$pid" 2>/dev/null || true
  echo "[driver] sigkill[s$ACTIVE]: killed PID $pid (comm=$comm, uuid=$UUID, lock=$lockfile)" >&2
  SES_OWNED[$ACTIVE]=0
  # Don't tmux kill-session — the dead-process pane stays so a later restart
  # cleans it, matching claudecode's own step_sigkill. The kill alone is what
  # produces process_exited; killing the pane too would not add a second
  # signal, only remove the diagnostic value of the leftover pane.
  sleep 1
}

# step_seed_instruction writes a persistent project-rules file into the run's
# cwd (e.g. AGENTS.md) before the agent ever launches, so it loads the text as
# a standing instruction the same way CLAUDE.md/AGENTS.md does for
# claudecode/codex (#1960 record pass — designed once for task-estimate-marker,
# ported into the shared template too so the other four waiting columns get it
# from the same source). Pure filesystem I/O — agent-agnostic by construction,
# unlike every tmux-keystroke arm above: `printf '%s' "$text" >
# "$RUN_CWD/$path"`. muse's own binary confirms AGENTS.md is its native
# per-project rules file (`muse init`'s template: "Muse Code reads this file
# as project rules when it runs in this directory").
step_seed_instruction() { # <path> <text>
  local path="$1" text="$2"
  printf '%s' "$text" > "$RUN_CWD/$path"
  echo "[driver] seed_instruction: wrote $(printf '%s' "$text" | wc -c | tr -d ' ') bytes to $RUN_CWD/$path" >&2
}

# --- Step dispatch: ALL standard arms present; stubs fail loudly -------------
# PRE-LAUNCH PASS (#1960): a persistent-rules file must exist BEFORE the
# agent's own process starts, or it is never loaded into the system-reminder
# the client injects at session open — but launch_repl below fires
# UNCONDITIONALLY, before this loop ever reads SCRIPT_JSON. Every 5-8
# task-estimate-marker recipe (the primitive's only consumer today) puts
# seed_instruction FIRST in its script for exactly this reason, so a bare
# in-loop case arm alone would run it too late — muse would already be
# running without the file. This eagerly executes any seed_instruction
# step(s) at the FRONT of SCRIPT_JSON before launch_repl runs; the in-loop
# case arm below calls the identical function so the primitive also works
# correctly (not just harmlessly-redundant) if a future recipe ever needs a
# mid-script instruction-file update instead of a leading one.
while IFS= read -r _leading_step; do
  [[ "$(jq -r '.type' <<<"$_leading_step")" == "seed_instruction" ]] || break
  step_seed_instruction "$(jq -r '.path' <<<"$_leading_step")" "$(jq -r '.text' <<<"$_leading_step")"
done < <(jq -c '.[]' <<<"$SCRIPT_JSON")

launch_repl
# shellcheck disable=SC2034  # read by the sourced replaydata/_lib/drive/slots.sh (save_active/load_slot)
EXPECTED_TURNS=0
while IFS= read -r step; do
  type="$(jq -r '.type' <<<"$step")"

  # Optional inline session target (pi pattern, driver-interactive.sh:513-528):
  # switch the active context to slot N before executing the step — e.g.
  # {"type":"send","text":"…","session":1} sends to session 1 after
  # start_session moved focus to a later slot. start_session is exempt (it
  # allocates its own slot); a target slot must already exist.
  tgt="$(jq -r '.session // empty' <<<"$step")"
  if [[ -n "$tgt" && "$type" != "start_session" && "$tgt" != "$ACTIVE" ]]; then
    if [[ "$tgt" =~ ^[0-9]+$ && "$tgt" -ge 1 && "$tgt" -le "$N_SLOTS" ]]; then
      save_active
      load_slot "$tgt"
      echo "[driver] switch -> session slot $tgt (uuid=$UUID)" >&2
    else
      echo "[driver] switch: invalid session slot '$tgt' (have $N_SLOTS)" >&2
      EXIT_REASON="nonzero(2)"
      break
    fi
  fi

  case "$type" in
    send|slash)      step_send "$(jq -r '.text' <<<"$step")" ;;
    wait_turn)       step_wait_turn || break ;;
    sleep)           sleep "$(jq -r '.seconds // 1' <<<"$step")" ;;
    interrupt)       step_interrupt ;;
    keys)            step_keys "$(jq -r '.keys' <<<"$step")" ;;
    reset_session)   step_reset_session "$(jq -r '.text // empty' <<<"$step")" || break ;;
    restart)         step_restart || break ;;
    resume)          step_resume || break ;;
    sigkill)         step_sigkill || break ;;
    exit_clean)      step_exit_clean ;;
    start_session)   step_start_session "$(jq -r '.cwd // empty' <<<"$step")" || break ;;
    session)         : ;;   # pure focus switch — already handled by the inline target block above
    seed_instruction) step_seed_instruction "$(jq -r '.path' <<<"$step")" "$(jq -r '.text' <<<"$step")" ;;
    *)               echo "[driver] unknown step type: $type" >&2; EXIT_REASON="nonzero(2)"; break ;;
  esac
  (( $(remaining_seconds) <= 0 )) && { EXIT_REASON="timeout"; break; }
done < <(jq -c '.[]' <<<"$SCRIPT_JSON")

# --- Write the staging contract (shared) -------------------------------------
# Persist the final active state, then best-effort resolve any slot that never
# got a transcript (e.g. a script with no wait_turn for that session) so
# session.uuids + transcript.paths are populated (pi pattern).
save_active
for (( i = 1; i <= N_SLOTS; i++ )); do
  if [[ -z "${SES_TRANSCRIPT[$i]}" ]]; then
    load_slot "$i"
    resolve_transcript || true
    save_active
  fi
done
# Staging contract: muse's primary session.uuid is the agent-side UUID (the
# session dir basename) for fixture-naming parity — run-cell.sh re-maps it to
# the daemon's session_id via its transcript_path lookup. daemon_sid of a muse
# transcript path is always the constant "session" (basename session.jsonl),
# so unlike gemini the shared uuids list CANNOT carry daemon-side ids until
# the Go adapter defines muse's session_id — record revisits this with the
# parser. drive_exit maps EXIT_REASON → the process exit code.
emit_session_contract "${SES_UUID[1]}"

# MULTI-SESSION FIX (#1960, this is "record revisits this" from the comment
# above): emit_session_contract's shared session.uuids line is
# `daemon_sid("${SES_TRANSCRIPT[$i]}")` — basename of the transcript path
# minus ".jsonl" — which disambiguates codex/pi (a unique filename per
# session) but NOT muse, whose transcript is always literally named
# "session.jsonl" for every slot: EVERY line collapsed to the same useless
# literal string "session", live-confirmed against a real 3-slot session-end
# recording (session.uuids held three identical "session" lines). run-cell.sh
# forwards those lines verbatim as IRRLICHT_EXTRA_SESSION_IDS, so curate
# searched for a session_id literally equal to "session", found nothing, and
# silently dropped slots 2 and 3 from events.jsonl entirely — the SAME
# curation-drop shape the assess/record skill's own anti-pattern list already
# names for a different adapter (1-5_session-reset), just reached a new way.
# The fix: overwrite session.uuids with each slot's OWN muse-native UUID
# (SES_UUID[i], the session directory's basename) — this genuinely IS the
# daemon's session_id for muse (confirmed: the curated events.jsonl's
# session_id for slot 1 is byte-identical to SES_UUID[1]), unlike the shared
# helper's basename-of-filename computation.
: > "$STAGING/session.uuids"
for (( i = 1; i <= N_SLOTS; i++ )); do
  echo "${SES_UUID[$i]}" >> "$STAGING/session.uuids"
done
echo "drive-muse-interactive: $EXIT_REASON (slots=${N_SLOTS}, primary=${SES_UUID[1]}, transcript=${SES_TRANSCRIPT[1]})"
# The epilogue completed: EXIT_REASON is this run's real verdict, so cleanup()
# must record it as-is rather than rewrite it as an abort.
REACHED_EPILOGUE=1
drive_exit
