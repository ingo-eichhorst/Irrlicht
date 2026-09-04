#!/usr/bin/env bash
# recording-profile.sh — profile-aware recording selection shared by replay
# gates and recording runs. Functions only; safe to source.

validate_execution_profile() {
  case "$1" in
    cli-local|desktop-local) return 0 ;;
    *) echo "unknown execution profile '$1' (allowed: 'cli-local', 'desktop-local')" >&2; return 2 ;;
  esac
}

# recording_execution_profile <recording-dir>
# Missing manifest/field is the legacy cli-local form. Present empty, null,
# non-string, and unknown values fail loudly.
recording_execution_profile() {
  local recording_dir="$1" manifest="$1/manifest.json" profile
  if [[ ! -f "$manifest" ]]; then
    printf '%s\n' cli-local
    return 0
  fi
  profile="$(jq -er '
    if has("execution_profile") then
      if (.execution_profile | type) == "string" and (.execution_profile | length) > 0
      then .execution_profile
      else error("execution_profile must be a non-empty string")
      end
    else "cli-local"
    end
  ' "$manifest")" || {
    echo "invalid execution profile in $manifest" >&2
    return 2
  }
  validate_execution_profile "$profile" || return $?
  printf '%s\n' "$profile"
}

# newest_recording_for_profile <cell-dir> <profile>
# Prints an empty string when no matching recording exists.
newest_recording_for_profile() {
  local cell_dir="$1" wanted="$2" recording_dir profile name newest_name="" newest_dir=""
  validate_execution_profile "$wanted" || return $?
  for recording_dir in "$cell_dir"/recordings/*/; do
    [[ -d "$recording_dir" ]] || continue
    profile="$(recording_execution_profile "${recording_dir%/}")" || return $?
    [[ "$profile" == "$wanted" ]] || continue
    name="$(basename "${recording_dir%/}")"
    if [[ -z "$newest_name" || "$name" > "$newest_name" ]]; then
      newest_name="$name"
      newest_dir="${recording_dir%/}"
    fi
  done
  printf '%s\n' "$newest_dir"
}
