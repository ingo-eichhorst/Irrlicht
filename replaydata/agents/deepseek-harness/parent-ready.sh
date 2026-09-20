#!/usr/bin/env bash
# Helpers for the DSH interactive recording driver. This file is sourced.

# dsh_await_parent_ready polls Irrlicht's live session snapshot until parent_id
# reaches ready. It fails distinctly when the snapshot cannot be read, parsed,
# or found. Arguments: <bind-address> <parent-id> <deadline-epoch-seconds>
dsh_await_parent_ready() {
  local bind_addr="$1" parent_id="$2" deadline="$3"
  local response state last_state="" first_scan=1
  [[ -n "$bind_addr" ]] || {
    echo "await_parent_ready: daemon bind address is empty" >&2
    return 1
  }
  while (( first_scan || $(date +%s) <= deadline )); do
    first_scan=0
    if ! response="$(curl -fsS --connect-timeout 1 --max-time 2 "http://$bind_addr/api/v1/sessions")"; then
      echo "await_parent_ready: could not read daemon session snapshot from $bind_addr" >&2
      return 1
    fi
    if ! jq -e '.groups | arrays' >/dev/null 2>&1 <<<"$response"; then
      echo "await_parent_ready: daemon session snapshot was malformed" >&2
      return 1
    fi
    state="$(jq -er --arg id "$parent_id" '.. | objects | select(.session_id? == $id) | .state' <<<"$response" 2>/dev/null | head -n1)" || {
      if (( $(date +%s) > deadline )); then
        echo "await_parent_ready: parent session $parent_id was absent from daemon snapshot before deadline" >&2
        return 1
      fi
      sleep 0.2
      continue
    }
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
