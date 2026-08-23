#!/usr/bin/env bash
# unapplied-grants-check.sh — refuse to drive an agent CLI whose hooks grant
# reads "granted" but was never actually applied (#1362's shape: the daemon
# accepted the grant and then failed, silently, to install it).
#
# run-cell.sh's ATTACH path has checked this since #1449 — a human might
# point --attach at a daemon whose install already failed. The SPAWN/isolated
# path — the one the recording rig itself drives on every ordinary run — never
# did, and that asymmetry is exactly how #1735's three-attempt mistral-vibe
# diagnosis happened: the daemon's own `unapplied_grants` already named the
# hooktoml refusal, and nothing on the path the rig actually uses ever asked
# it (#1754).
#
# THE RACE ON THE SPAWN PATH (found by review during #1754 itself — an
# earlier draft of this file claimed the opposite and was wrong). Traced
# through core/cmd/irrlichd/{main,startup}.go and
# core/application/services/permission_service.go: the unix socket is bound,
# and its accept-loop goroutine launched, well BEFORE `startBackgroundLoops`
# is reached — which is where `PermService.Start(ctx)` actually runs, still
# on main()'s own goroutine, synchronously. Start() marks every grant-all
# permission Granted immediately, then calls runEffects() — OUTSIDE its own
# lock — which is what actually writes each adapter's hook config to disk and
# only THEN records success/failure into effectErrs (recordEffectResult,
# called synchronously right after each effect closure returns). A
# GET /api/v1/permissions arriving after the socket is up but before
# runEffects reaches (or finishes) this adapter's hook effect sees the
# permission already state=granted with effectErrs still empty — which reads
# as "no unapplied grants" for the same reason an unset field would: the
# question hasn't been evaluated yet, not because it was evaluated and passed.
# #1735 measured this Apply pass taking ~4s for mistral-vibe's hooktoml write.
#
# So a single unpolled check on the spawn path can read "clean" during
# exactly the window it exists to catch. wait_for_unapplied_grants_clear
# below polls it instead: any OBSERVED unapplied grant is a real, permanent-
# for-this-boot fact (effectErrs is never cleared once set) and refuses
# immediately: only "everything above looked fine" is the ambiguous reading,
# so that is the one this loop declines to trust on a single sample.
#
# Usage — after the daemon (attached OR spawned) is confirmed up:
#   source "$SCRIPT_DIR/lib/unapplied-grants-check.sh"
#   check_unapplied_grants "$ONBOARD_BIND" "$ADAPTER" ["$PERM_JSON"] || exit 1  # attach: daemon has been up for a while, Start() is long done
#   wait_for_unapplied_grants_clear "$ONBOARD_BIND" "$ADAPTER" || exit 1        # spawn: daemon just started, Start() may still be mid-flight

# check_unapplied_grants <bind> <adapter> [<perm_json>] queries the daemon's
# permission snapshot ONCE and refuses (prints why, returns 1) if IT names an
# unapplied grant for adapter. A daemon too old to expose the field, or an
# unreachable endpoint, reads as "nothing to check" (0) — the same "can't
# tell" case run-cell.sh's original attach-only version already treated as a
# pass.
#
# <perm_json>, if given non-empty, is used AS-IS instead of a fresh curl — the
# attach path already fetched this same endpoint one line above for its own
# PERM_OK gate, and refetching it here would be a second identical loopback
# round-trip for no new information. wait_for_unapplied_grants_clear never
# passes it: a poll's whole point is a FRESH sample each tick.
#
# A single sample is correct on the ATTACH path (the daemon predates this
# script run, so Start() is not in flight) but NOT on the spawn path — see
# wait_for_unapplied_grants_clear for why, and use that instead there.
check_unapplied_grants() {
  local bind="$1" adapter="$2" perm_json="${3:-}" unapplied
  if [[ -z "$perm_json" ]]; then
    # $bind is a loopback host:port for a daemon this rig started itself; the
    # daemon's local API is plain HTTP by design — there is no TLS listener to
    # point at instead. shell:S5332 cannot resolve the variable to see that, so
    # it is annotated, the same NOSONAR shape as run-cell.sh's attach-path fetch
    # and hook-install-wait.sh's failure-path one.
    perm_json="$(curl -fsS --max-time 2 "http://$bind/api/v1/permissions" 2>/dev/null || true)" # NOSONAR (shell:S5332) — loopback-only daemon API
  fi
  [[ -n "$perm_json" ]] || return 0
  # The filter is a variable so the jq call stays ONE PHYSICAL LINE: a NOSONAR
  # annotation only suppresses the line it sits on, and Sonar's taint tracking
  # carries S5332 from the curl above into every reader of $perm_json.
  local filter='[.unapplied_grants // [] | .[] | select(.agent == $a) | "\(.agent)/\(.key): \(.reason)"] | join("; ")'
  unapplied="$(jq -r --arg a "$adapter" "$filter" <<<"$perm_json" 2>/dev/null || echo "")" # NOSONAR (shell:S5332) — reads the loopback response above
  [[ -n "$unapplied" ]] || return 0
  echo "unapplied-grants-check: daemon at $bind has grants that were NOT applied for $adapter" >&2 # NOSONAR (shell:S5332) — names the loopback daemon above
  echo "unapplied-grants-check:   $unapplied" >&2 # NOSONAR (shell:S5332) — echoes the loopback response above
  echo "unapplied-grants-check:   it would record a fixture in which $adapter merely looks incapable of reporting state" >&2
  echo "unapplied-grants-check:   if this is the #1449 shared-config refusal: back up the files 'irrlichd --print-managed-files' names," >&2
  echo "unapplied-grants-check:   then restart with IRRLICHT_ALLOW_SHARED_CONFIG_WRITES=1 (already set on the spawn path here — look for a version-floor or auth refusal instead)" >&2
  return 1
}

# Poll knobs, read at call time; the defaults ARE the production timing (20 ×
# 0.5s = 10s, more than double #1735's measured ~4s Apply latency) and are
# overridable only so unit tests need not spend them in real seconds — the
# same shape as hook-install-wait.sh's HOOK_INSTALL_WAIT_TICKS.
UNAPPLIED_GRANTS_WAIT_TICKS="${UNAPPLIED_GRANTS_WAIT_TICKS:-20}"
UNAPPLIED_GRANTS_WAIT_TICK_S="${UNAPPLIED_GRANTS_WAIT_TICK_S:-0.5}"

# wait_for_unapplied_grants_clear <bind> <adapter> polls check_unapplied_grants
# instead of sampling it once, because runEffects (see the header) can still
# be mid-flight for a daemon this run just spawned. Any refusal is trusted the
# INSTANT it is observed — effectErrs is never cleared once written this boot,
# so a positive reading can only be real. A clean reading is trusted only
# after the poll ceiling passes with no refusal ever seen; that is a deadline,
# not proof, for the same reason wait_for_hook_install's own ceiling is not
# proof — this file's header says so rather than overclaiming certainty.
wait_for_unapplied_grants_clear() {
  local bind="$1" adapter="$2" waited=0
  while (( waited < UNAPPLIED_GRANTS_WAIT_TICKS )); do
    if ! check_unapplied_grants "$bind" "$adapter"; then
      return 1
    fi
    waited=$((waited + 1))
    (( waited < UNAPPLIED_GRANTS_WAIT_TICKS )) && sleep "$UNAPPLIED_GRANTS_WAIT_TICK_S"
  done
  return 0
}
