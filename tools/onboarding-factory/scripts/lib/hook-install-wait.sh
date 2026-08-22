#!/usr/bin/env bash
# hook-install-wait.sh — do not launch the agent CLI until the recording
# daemon has finished INSTALLING that adapter's hooks.
#
# THE RACE. spawn_record_daemon returns as soon as the daemon's unix socket
# exists. The socket is bound before the permission service has run its
# grant-all Apply closures, so for a few seconds the daemon is up and the
# adapter's hook config has not been written yet. run-cell.sh then launches the
# CLI immediately — and every agent CLI here reads its hook config ONCE, at
# startup. The recording that comes back is a real, complete, healthy-looking
# recording of a session with no hooks in it.
#
# Measured on this machine (#1735, mistral-vibe 2-1_basic-turn): the run
# completed with driver_exit_reason=ok and completeness=complete, the daemon
# recorded 11 events for the session, `of validate` would have accepted it —
# and there was not a single hook_received event anywhere in the raw capture,
# because $VIBE_HOME/hooks.toml appeared about four seconds after vibe had
# already read it. Promoting that fixture would have frozen "mistral-vibe
# reports no hooks" as catalog truth.
#
# This is the same rule #1449 wrote into ir:exec's Phase 4 step 11: a readiness
# signal narrower than the thing under test fires BEFORE the consent effects
# run, so a deliberately broken binary comes back green.
#
# WHAT IT WAITS ON, and why not a sleep. `sleep 5` would have fixed today's
# symptom and observed nothing — per docs/testing-philosophy.md, a fixture that
# waits by sleeping has not observed what it waits for. So it polls for the
# files themselves: the managed-file snapshot has already asked the daemon which
# files it manages and recorded, per path, whether it existed BEFORE the daemon
# started (lib/managed-file-snapshot.sh writes that manifest). A declared path
# that was `absent` and is expected to be written by this adapter's install is
# exactly the observable "the install has landed".
#
# WHAT IT CANNOT SEE, said out loud rather than left to be discovered. The
# manifest is a flat list of paths with no adapter attribution, so this can only
# select an adapter's own files when that adapter has a rig-home row
# (lib/agent-home.sh) and the operator exported it — then its files are the ones
# under $VAR. With no isolated home there is nothing to select on, and it says
# so and waits for nothing rather than reporting a check it did not perform.
#
# Usage — after spawn_record_daemon, before the driver runs:
#   source "$SCRIPT_DIR/lib/hook-install-wait.sh"
#   wait_for_hook_install "$ADAPTER" "$STAGING" "$ONBOARD_BIND" || exit 1

# Poll knobs, read at call time; the defaults ARE the production timings and are
# overridable only so the unit tests need not spend them in real seconds.
# 60 × 0.5s = 30s, generously above the ~4s observed for a vibe install.
HOOK_INSTALL_WAIT_TICKS="${HOOK_INSTALL_WAIT_TICKS:-60}"
HOOK_INSTALL_WAIT_TICK_S="${HOOK_INSTALL_WAIT_TICK_S:-0.5}"

# hook_install_pending_paths <staging> <home-prefix> prints every path the
# snapshot manifest recorded as `absent` that lives under home-prefix and does
# not exist yet. Empty output means nothing is outstanding.
hook_install_pending_paths() {
  local staging="$1" prefix="$2"
  local manifest="$staging/managed-file-backup/manifest"
  [[ -f "$manifest" ]] || return 0
  # The manifest's middle field is a backup-slot number nothing here needs, so
  # it is projected away rather than read into a variable that would then be
  # unused — which is both a shellcheck SC2034 and a Sonar shelldre:S1481, and
  # a disable comment for each is worse than not creating the variable.
  local state path
  while IFS=$'\t' read -r state path; do
    [[ "$state" == "absent" ]] || continue
    [[ "$path" == "$prefix"/* ]] || continue
    [[ -e "$path" ]] && continue
    printf '%s\n' "$path"
  done < <(awk -F'\t' '{ print $1 "\t" $3 }' "$manifest")
  return 0
}

# hook_install_declared_under <staging> <home-prefix> counts the manifest rows
# under home-prefix, in ANY state. It is the vacuity guard, kept separate from
# the verdict so "this adapter declares no files here" cannot be confused with
# "everything this adapter declares has arrived" — the two produce identical
# empty pending lists and mean opposite things.
hook_install_declared_under() {
  local staging="$1" prefix="$2"
  local manifest="$staging/managed-file-backup/manifest"
  [[ -f "$manifest" ]] || { printf '0\n'; return 0; }
  awk -F'\t' -v p="$prefix/" 'substr($3, 1, length(p)) == p { n++ } END { print n + 0 }' "$manifest"
  return 0
}

# wait_for_hook_install <adapter> <staging> [<bind-addr>] blocks until every
# managed file this adapter's install is expected to create exists, or the
# deadline passes. Returns non-zero only in the case that is certain to produce
# a lying fixture: files were expected, they never arrived, and the daemon does
# not name a refusal that explains it.
wait_for_hook_install() {
  local adapter="${1:-}" staging="${2:-}" bind="${3:-}"
  local var prefix declared pending waited=0

  if [[ -z "$adapter" || -z "$staging" ]]; then
    echo "hook-install-wait: usage: wait_for_hook_install <adapter> <staging> [<bind-addr>]" >&2
    return 1
  fi

  var="$(agent_home_var "$adapter")"
  prefix="${var:+${!var:-}}"
  if [[ -z "$prefix" ]]; then
    echo "hook-install-wait: $adapter — no isolated home is exported, so this adapter's managed files"
    echo "hook-install-wait:   cannot be told from every other adapter's in the manifest. NOT waiting;"
    echo "hook-install-wait:   if the CLI reads its hook config at startup, this run can race the install."
    return 0
  fi

  declared="$(hook_install_declared_under "$staging" "$prefix")"
  if [[ "$declared" == "0" ]]; then
    echo "hook-install-wait: $adapter — the daemon declares no managed file under $prefix;"
    echo "hook-install-wait:   there is nothing to wait for and nothing was checked."
    return 0
  fi

  while (( waited < HOOK_INSTALL_WAIT_TICKS )); do
    pending="$(hook_install_pending_paths "$staging" "$prefix")"
    if [[ -z "$pending" ]]; then
      echo "hook-install-wait: $adapter — all $declared managed file(s) under $prefix are present after $(awk -v w="$waited" -v t="$HOOK_INSTALL_WAIT_TICK_S" 'BEGIN{printf "%.1f", w*t}')s"
      return 0
    fi
    waited=$((waited + 1))
    sleep "$HOOK_INSTALL_WAIT_TICK_S"
  done

  echo "hook-install-wait: $adapter — these declared files never appeared:" >&2
  printf 'hook-install-wait:   %s\n' $pending >&2
  # A refusal the daemon can name is a different failure from a silent one, and
  # only the silent one is this lib's business. Printing it here is what stops
  # the operator debugging the wait instead of the version floor / auth error
  # that actually stopped the install (#1362, #1365).
  #
  # $bind is a loopback host:port for a daemon this rig started itself, and the
  # daemon's local API is plain HTTP by design — there is no TLS listener to
  # point at instead. shell:S5332 cannot resolve the variable to see that, so
  # it is annotated, exactly as run-cell.sh's own permissions fetch is.
  #
  # The FILTER IS A VARIABLE so the curl stays ONE PHYSICAL LINE. That is not
  # style: a NOSONAR annotation suppresses only the line it sits on, so a call
  # split across a continuation carries the finding on the first line while the
  # annotation rides the second. This file's first draft did exactly that and
  # turned SonarCloud's security rating red — which is this lib's own
  # half-wired-lever failure reproduced inside the fix for it. run-cell.sh
  # carries the identical note above the fetch it learned it from.
  if [[ -n "$bind" ]]; then
    local reasons_filter='[(.unapplied_grants // [])[] | select(.agent == $a) | "\(.key): \(.reason)"] | join("; ")'
    local reasons
    reasons="$(curl -fsS --max-time 2 "http://$bind/api/v1/permissions" 2>/dev/null | jq -r --arg a "$adapter" "$reasons_filter" 2>/dev/null || true)" # NOSONAR (shell:S5332) — loopback-only daemon API, no TLS listener exists
    if [[ -n "$reasons" ]]; then
      echo "hook-install-wait:   the daemon names a refusal: $reasons" >&2 # NOSONAR (shell:S5332) — echoes the loopback response above
    else
      echo "hook-install-wait:   the daemon names NO refusal, so the install neither ran nor failed loudly." >&2
    fi
  fi
  echo "hook-install-wait: refusing to drive $adapter — the CLI reads its hook config at startup, so this" >&2
  echo "hook-install-wait:   run would record a healthy-looking fixture with no hook events in it." >&2
  return 1
}
