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
# unapplied_grants is populated synchronously by PermissionService.Start's
# grant-all Apply pass, which has already run by the time spawn_record_daemon
# returns (the socket only comes up after Start finishes) — so this needs no
# poll of its own. That is deliberately different from
# wait_for_hook_install, which watches for the INSTALLED FILES to land and
# can legitimately take several seconds: this check is the fast, NAMED
# diagnosis; wait_for_hook_install is the slower file-presence backstop for
# whatever this cannot see (e.g. a daemon too old to expose the field).
#
# Usage — after the daemon (attached OR spawned) is confirmed up:
#   source "$SCRIPT_DIR/lib/unapplied-grants-check.sh"
#   check_unapplied_grants "$ONBOARD_BIND" "$ADAPTER" || exit 1

# check_unapplied_grants <bind> <adapter> queries the daemon's permission
# snapshot and refuses (prints why, returns 1) if IT names an unapplied grant
# for adapter. A daemon too old to expose the field, or an unreachable
# endpoint, reads as "nothing to check" (0) — the same "can't tell" case
# run-cell.sh's original attach-only version already treated as a pass.
check_unapplied_grants() {
  local bind="$1" adapter="$2" perm_json unapplied
  # $bind is a loopback host:port for a daemon this rig started itself; the
  # daemon's local API is plain HTTP by design — there is no TLS listener to
  # point at instead. shell:S5332 cannot resolve the variable to see that, so
  # it is annotated, the same NOSONAR shape as run-cell.sh's attach-path fetch
  # and hook-install-wait.sh's failure-path one.
  perm_json="$(curl -fsS --max-time 2 "http://$bind/api/v1/permissions" 2>/dev/null || true)" # NOSONAR (shell:S5332) — loopback-only daemon API
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
