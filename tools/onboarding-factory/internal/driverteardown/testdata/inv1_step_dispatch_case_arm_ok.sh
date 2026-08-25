#!/usr/bin/env bash
# MUST NOT BE FLAGGED — copilot's shape, and the case that forced INV-1's
# classification to be narrowed.
#
# copilot's step dispatch is a TOP-LEVEL `while … case`, not a function, so its
# per-step kills sit at Func == "" and a rule reading "top level" as "the
# end-of-run sweep" graded them as teardown. They are ungated in copilot today
# so nothing fired — but the kiro-cli-shaped SES_ALIVE entry check that this
# package deliberately ACCEPTS at step level (correct there, because
# reset_session aliases a retired slot onto a live pane) would have been a false
# positive the moment copilot, hermes, mistral-vibe or antigravity needed one.
# This fixture is that moment: the `restart` arm carries the guard.
#
# The end-of-run sweep below is the real teardown and is ungated, so the file is
# clean — which is only meaningful because inv1_gated_final_sweep.sh and
# inv1_gated_case_at_exit.sh still fire.
set -euo pipefail
_DRIVE_LIB="$(dirname "${BASH_SOURCE[0]}")/lib"
source "$_DRIVE_LIB/slots.sh"

SESSION=""
SES_SESSION=()
SES_CWD=()
SES_ALIVE=()
N_SLOTS=0
ACTIVE=0
EXIT_REASON="ok"
REACHED_EPILOGUE=0
RUN_CWD="$STAGING/cwd"

cleanup() {
  if [[ "$REACHED_EPILOGUE" != "1" && "$EXIT_REASON" == "ok" ]]; then
    EXIT_REASON="nonzero(2)"
  fi
  local i
  for (( i = 1; i <= N_SLOTS; i++ )); do
    [[ -n "${SES_SESSION[$i]:-}" ]] && tmux kill-session -t "${SES_SESSION[$i]}" 2>/dev/null || true
  done
  echo "$EXIT_REASON" > "$STAGING/driver.exit-reason"
}
trap cleanup EXIT

launch_repl() {
  alloc_slot "fixture-onboard-$(date +%s)-$$-$((N_SLOTS + 1))" "$RUN_CWD"
  tmux new-session -d -s "$SESSION" -x 200 -y 50 -c "${SES_CWD[$ACTIVE]}" "fixture-agent"
}

launch_repl
while IFS= read -r step; do
  type="$(jq -r '.type' <<<"$step")"
  case "$type" in
    restart)     if [[ "${SES_ALIVE[$ACTIVE]:-0}" == "1" ]]; then
                   tmux kill-session -t "$SESSION" 2>/dev/null || true
                 fi
                 launch_repl ;;
    send)        tmux send-keys -t "$SESSION" "$(jq -r '.text' <<<"$step")" ;;
    *)           echo "[driver] unknown step type: $type" >&2 ;;
  esac
done < <(jq -c '.[]' <<<"$SCRIPT_JSON")

# Tear down every session this run created.
for (( i = 1; i <= N_SLOTS; i++ )); do
  if [[ -n "${SES_SESSION[$i]:-}" ]]; then
    tmux kill-session -t "${SES_SESSION[$i]}" 2>/dev/null || true
  fi
done

# The epilogue completed: EXIT_REASON is this run's real verdict.
REACHED_EPILOGUE=1
