#!/usr/bin/env bash
# promote-hookcheck.sh — the promote-time hook-coverage prompt (#1754).
#
# A hooks-declaring adapter's staged recording that carries no hook_received
# event attributable to it is exactly the failure mode #1735 took three
# attempts to diagnose: the recording completes, driver_exit_reason=ok,
# completeness=complete, and nothing anywhere says the hook channel under
# test never fired. promote-recording.sh is the LAST gate before a fixture
# becomes committed truth, so this is where the question finally gets asked.
#
# A hook-free recording of a hooks-declaring adapter is sometimes legitimate —
# the scenario genuinely produces no hook, and `of coverage --hooks` already
# reports the corpus-wide population — so this is a PROMPT / explicit
# override, never a hard failure: the operator must say "yes, intended"
# rather than being blocked outright.
#
# promote_hookcheck takes check_fn and confirm_fn BY NAME (bash resolves a
# bare word to a shell function, same as atomic-promote.sh's injected
# populate/validate functions) so promote-hookcheck_test.sh can pin the
# decision table with fakes — no `go run`, no real terminal, no live daemon.
#
#   check_fn agent events_path   -> prints "<declares_hooks> <has_hook_event>"
#                                    (each the literal word true/false) on
#                                    stdout and exits 0; a non-zero exit means
#                                    the check itself could not answer, and
#                                    its stdout+stderr is the diagnostic.
#   confirm_fn (no args)         -> exit 0 = operator confirmed intended,
#                                    non-zero = refused, declined, or no
#                                    override available (e.g. non-interactive
#                                    with nothing set).
#
# Returns:
#   0 — promote (either the healthy case, or the operator confirmed intended)
#   1 — refuse: hook-free recording of a hooks-declaring adapter, not confirmed
#   2 — refuse: check_fn itself failed to run. AGENTS.md: "a verification
#       mechanism must fail loudly when it cannot run" — a broken check must
#       never silently read as "nothing to see", so this is a REFUSAL, not a
#       skip.
#
# Usage — after the staged events.jsonl is known to exist:
#   source "$SCRIPT_DIR/lib/promote-hookcheck.sh"
#   promote_hookcheck "$AGENT" "$STAGED_DIR/events.jsonl" \
#     default_hookfree_check default_hookfree_confirm || exit 1
promote_hookcheck() {
  local agent="$1" events_path="$2" check_fn="$3" confirm_fn="$4"
  local result declares has_hook

  if ! result="$("$check_fn" "$agent" "$events_path")"; then
    echo "promote: hook-coverage check failed to run — refusing to promote blind" >&2
    [[ -n "$result" ]] && echo "$result" >&2
    return 2
  fi
  declares="$(awk '{print $1}' <<<"$result")"
  has_hook="$(awk '{print $2}' <<<"$result")"

  [[ "$declares" == "true" ]] || return 0    # doesn't declare hooks — nothing to ask
  [[ "$has_hook" != "true" ]] || return 0    # declares hooks AND has one — the healthy case

  echo "WARNING: $agent declares a hooks permission, but this recording carries no" >&2
  echo "         hook_received event attributable to it — the failure mode #1735" >&2
  echo "         took three attempts to diagnose (a complete, healthy-looking" >&2
  echo "         recording whose hook channel never fired, with nothing anywhere" >&2
  echo "         saying so)." >&2
  echo "         Legitimate when the scenario genuinely produces no hook — run" >&2
  echo "         'of coverage --hooks' if unsure whether this is the corpus norm." >&2

  if "$confirm_fn"; then
    echo "         confirmed intended — promoting." >&2
    return 0
  fi
  echo "promote: refusing a hook-free recording of a hooks-declaring adapter" >&2
  return 1
}

# default_hookfree_check <agent> <events_path> — the real check_fn: shells out
# to `of hookcheck`, which joins the daemon's adapter registry (declares
# hooks?) against THIS events.jsonl's own session-attributed hook_received
# events (has one?). go run compiles from source, matching how this same
# script already invokes ./cmd/expected-validate.
default_hookfree_check() {
  local agent="$1" events_path="$2" json declares has_hook
  if ! json="$(cd "$REPO_ROOT" && go run ./tools/onboarding-factory/cmd/of hookcheck --agent "$agent" --events "$events_path" 2>&1)"; then
    echo "$json"
    return 1
  fi
  if ! declares="$(jq -r '.declares_hooks' <<<"$json" 2>/dev/null)" || [[ -z "$declares" ]]; then
    echo "of hookcheck produced unparseable output: $json"
    return 1
  fi
  if ! has_hook="$(jq -r '.has_hook_event' <<<"$json" 2>/dev/null)" || [[ -z "$has_hook" ]]; then
    echo "of hookcheck produced unparseable output: $json"
    return 1
  fi
  echo "$declares $has_hook"
}

# default_hookfree_confirm — the real confirm_fn: an explicit non-interactive
# override (IRRLICHT_PROMOTE_HOOKFREE_OK, for a scripted/agent-driven
# promote), or an interactive y/N prompt when connected to a real terminal.
# Anything else — non-interactive with no override set — refuses, because
# that is precisely the shape of "nobody looked at this."
default_hookfree_confirm() {
  if [[ -n "${IRRLICHT_PROMOTE_HOOKFREE_OK:-}" ]]; then
    echo "         IRRLICHT_PROMOTE_HOOKFREE_OK is set." >&2
    return 0
  fi
  if [[ -t 0 ]]; then
    local ans
    read -r -p "         promote this hook-free recording anyway? [y/N] " ans
    case "$ans" in
      y|Y|yes|YES) return 0 ;;
      *) return 1 ;;
    esac
  fi
  echo "         non-interactive and IRRLICHT_PROMOTE_HOOKFREE_OK is not set." >&2
  return 1
}
