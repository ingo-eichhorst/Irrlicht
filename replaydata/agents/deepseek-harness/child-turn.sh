#!/usr/bin/env bash
# Helpers for the DSH interactive recording driver. This file is sourced.

# dsh_await_child_turn_end waits until a durable native child of parent_id has
# a completed turn/end record. It prints the matching transcript path.
# Arguments: <sessions-dir> <parent-id> <marker-path> <deadline-epoch-seconds>
dsh_await_child_turn_end() (
  local sessions_dir="$1" parent_id="$2" marker_path="$3" deadline="$4"
  local candidate child_id decoded candidates header header_id header_parent origin
  local saw_child=0 saw_wrong_parent=0 saw_unreadable=0 saw_malformed=0
  if [[ ! -f "$marker_path" || ! -r "$marker_path" ]]; then
    echo "await_child_turn_end: marker is not a readable regular file: $marker_path" >&2
    return 1
  fi
  if [[ ! -d "$sessions_dir" || ! -r "$sessions_dir" || ! -x "$sessions_dir" ]]; then
    echo "await_child_turn_end: sessions directory is not readable: $sessions_dir" >&2
    return 1
  fi
  decoded="$(mktemp "${TMPDIR:-/tmp}/irrlicht-dsh-child-turn.XXXXXX")" || {
    echo "await_child_turn_end: cannot allocate decode buffer" >&2
    return 1
  }
  candidates="$(mktemp "${TMPDIR:-/tmp}/irrlicht-dsh-child-list.XXXXXX")" || {
    rm -f "$decoded"
    echo "await_child_turn_end: cannot allocate transcript list" >&2
    return 1
  }
  trap 'rm -f "$decoded" "$candidates"' EXIT

  while (( $(date +%s) <= deadline )); do
    if ! find "$sessions_dir" -type f -name 'session.v*.jsonl.zstd' -newer "$marker_path" -print >"$candidates"; then
      echo "await_child_turn_end: could not scan sessions directory: $sessions_dir" >&2
      return 1
    fi
    while IFS= read -r candidate; do
      child_id="$(basename "$(dirname "$candidate")")"
      [[ "$child_id" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$ ]] || continue
      if ! zstd -qdc "$candidate" >"$decoded" 2>/dev/null; then
        saw_unreadable=1
        continue
      fi
      if ! jq -e -s 'length > 0 and all(.[]; type == "object")' "$decoded" >/dev/null 2>&1; then
        saw_malformed=1
        continue
      fi
      header="$(sed -n '1p' "$decoded")"
      header_id="$(jq -r '.id // empty' <<<"$header" 2>/dev/null)"
      header_parent="$(jq -r '.parentSession // empty' <<<"$header" 2>/dev/null)"
      origin="$(jq -r '.origin // empty' <<<"$header" 2>/dev/null)"
      [[ "$(jq -r '.type // empty' <<<"$header" 2>/dev/null)" == session ]] || continue
      [[ "$header_id" == "$child_id" && "$origin" == subagent ]] || continue
      if [[ "$header_parent" != "$parent_id" ]]; then
        saw_wrong_parent=1
        continue
      fi
      saw_child=1
      if jq -e 'select(.type == "turn/end" and .data.reason.kind == "completed")' "$decoded" >/dev/null; then
        printf '%s\n' "$candidate"
        return 0
      fi
    done < "$candidates"
    sleep 0.2
  done

  if (( saw_child )); then
    echo "await_child_turn_end: child linked to $parent_id was found, but completed turn/end was not observed before deadline" >&2
  elif (( saw_wrong_parent )); then
    echo "await_child_turn_end: native child header had a different parent, not $parent_id" >&2
  elif (( saw_unreadable )); then
    echo "await_child_turn_end: native child transcript was unreadable before deadline" >&2
  elif (( saw_malformed )); then
    echo "await_child_turn_end: native child transcript had malformed JSON before deadline" >&2
  else
    echo "await_child_turn_end: no durable native child linked to $parent_id appeared before deadline" >&2
  fi
  return 1
)
