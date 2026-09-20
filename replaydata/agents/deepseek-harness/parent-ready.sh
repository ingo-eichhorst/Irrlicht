#!/usr/bin/env bash
# Helpers for the DSH interactive recording driver. This file is sourced.

# dsh_await_parent_ready polls Irrlicht's live session snapshot until parent_id
# reaches ready. It fails distinctly when the snapshot cannot be read, parsed,
# or found. Arguments: <bind-address> <parent-id> <deadline-epoch-seconds>
dsh_await_parent_ready() {
  local bind_addr="$1" parent_id="$2" deadline="$3"
  local port daemon_url response state last_state="" first_scan=1
  if [[ ! "$bind_addr" =~ ^127\.0\.0\.1:([0-9]+)$ ]]; then
    echo "await_parent_ready: daemon bind address must be 127.0.0.1 with a numeric unprivileged port" >&2
    return 1
  fi
  port="${BASH_REMATCH[1]}"
  if (( 10#$port < 1024 || 10#$port > 65535 )); then
    echo "await_parent_ready: daemon bind address must use an unprivileged port" >&2
    return 1
  fi
  # parent-ready_test.sh exercises the rejected address forms before curl runs.
  daemon_url="http://127.0.0.1:$port" # NOSONAR: the validated literal is loopback-only.
  while (( first_scan || $(date +%s) <= deadline )); do
    first_scan=0
    if ! response="$(curl -fsS --connect-timeout 1 --max-time 2 "$daemon_url/api/v1/sessions")"; then
      echo "await_parent_ready: could not read daemon session snapshot from $bind_addr" >&2
      return 1
    fi
    if ! jq -e '.groups | arrays' >/dev/null 2>&1 <<<"$response"; then
      echo "await_parent_ready: daemon session snapshot was malformed" >&2
      return 1
    fi
    state="$(jq -er --arg id "$parent_id" 'first(.. | objects | select(.session_id? == $id) | .state)' <<<"$response" 2>/dev/null)" || {
      if (( $(date +%s) > deadline )); then
        echo "await_parent_ready: parent session $parent_id was absent from daemon snapshot before deadline" >&2
        return 1
      fi
      sleep 0.2
      continue
    }
    case "$state" in
      ready|working|waiting|error) ;;
      *)
        echo "await_parent_ready: parent session $parent_id had malformed state $state" >&2
        return 1
        ;;
    esac
    last_state="$state"
    [[ "$state" == ready ]] && {
      echo "[driver] parent session ready: $parent_id" >&2
      return 0
    }
    if (( $(date +%s) > deadline )); then
      echo "await_parent_ready: parent session $parent_id remained $state before deadline" >&2
      return 1
    fi
    sleep 0.2
  done
  if [[ -n "$last_state" ]]; then
    echo "await_parent_ready: parent session $parent_id remained $last_state before deadline" >&2
  else
    echo "await_parent_ready: parent session $parent_id was absent from daemon snapshot before deadline" >&2
  fi
  return 1
}
