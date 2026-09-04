#!/usr/bin/env bash
# Claude Desktop Local profile guards and evidence staging.

desktop_shared_lock_path() {
  local repo_root="$1" common_dir clone_root build_root
  common_dir="$(git -C "$repo_root" rev-parse --path-format=absolute --git-common-dir 2>/dev/null)" || {
    echo "desktop-local cannot resolve the shared Git directory" >&2
    return 1
  }
  [[ "$(basename "$common_dir")" == ".git" && -d "$common_dir" && ! -L "$common_dir" ]] || {
    echo "desktop-local shared Git directory is invalid: $common_dir" >&2
    return 1
  }
  clone_root="$(cd "$(dirname "$common_dir")" && pwd -P)" || return 1
  build_root="$clone_root/.build"
  mkdir -p "$build_root" || return 1
  [[ ! -L "$build_root" && "$(cd "$build_root" && pwd -P)" == "$build_root" ]] || {
    echo "desktop-local shared .build must not be a symlink: $build_root" >&2
    return 1
  }
  printf '%s\n' "$build_root/claude-desktop-driver.lock"
}

# desktop_acquire_clone_lock keeps descriptor 9 open in the calling shell.
# The BSD lock therefore spans all later setup, Desktop control, and EXIT-trap
# cleanup. All linked worktrees resolve the same main-worktree .build path.
desktop_acquire_clone_lock() {
  local repo_root="$1"
  local lock_path
  lock_path="$(desktop_shared_lock_path "$repo_root")" || return 1
  exec 9>"$lock_path" || return 1
  if ! /usr/bin/lockf -s -t 0 9; then
    exec 9>&-
    echo "desktop-local another run holds the clone-wide Desktop lock: $lock_path" >&2
    return 1
  fi
  return 0
}

desktop_profile_validate_cell() {
  local profile="$1" adapter="$2" attach="$3" cell_json="$4"
  [[ "$profile" == "desktop-local" ]] || return 0
  if [[ "$adapter" != "claudecode" ]]; then
    echo "desktop-local is only supported for claudecode" >&2
    return 1
  fi
  if [[ "$attach" == "1" ]]; then
    echo "desktop-local requires its own recording daemon; --attach is not supported" >&2
    return 1
  fi
  if [[ -n "$(jq -c '.script // empty' <<<"$cell_json")" ]]; then
    echo "desktop-local supports one prompt only; script recipes are not supported" >&2
    return 1
  fi
  if [[ "$(jq -c '.settings // {}' <<<"$cell_json")" != "{}" ]] ||
     [[ "$(jq -c '.env // {}' <<<"$cell_json")" != "{}" ]] ||
     [[ "$(jq -c '.mock // empty' <<<"$cell_json")" != "" ]] ||
     [[ "$(jq -r '.bare_mode // false' <<<"$cell_json")" == "true" ]]; then
    echo "desktop-local refuses settings, env, mock, and bare-mode changes" >&2
    return 1
  fi
  return 0
}

desktop_choose_loopback_address() {
  /usr/bin/python3 -c '
import socket
s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
s.bind(("127.0.0.1", 0))
print("127.0.0.1:%d" % s.getsockname()[1])
s.close()
'
}

desktop_require_free_loopback_address() {
  local address="$1" host="${1%:*}" port="${1##*:}"
  [[ "$host" == "127.0.0.1" && "$port" =~ ^[0-9]+$ ]] || {
    echo "desktop-local requires a numeric 127.0.0.1 loopback address (got $address)" >&2
    return 1
  }
  if lsof -nP -iTCP:"$port" -sTCP:LISTEN >/dev/null 2>&1; then
    echo "desktop-local selected loopback port $port but it is no longer free" >&2
    return 1
  fi
  return 0
}

desktop_stage_evidence() {
  local source_dir="$1" recording="$2" session_id="$3" workspace="$4" destination="$5"
  local name source target
  if [[ -L "$source_dir" || -L "$destination" ]]; then
    echo "desktop-local evidence directories must not be symlinks" >&2
    return 1
  fi
  mkdir -p "$destination" || return 1
  [[ -d "$destination" && ! -L "$destination" ]] || return 1
  for name in desktop-registry.json desktop-environment.json process.json \
    irrlicht-session.json transcript.jsonl hooks.jsonl; do
    target="$destination/$name"
    if [[ -L "$target" || ( -e "$target" && ! -f "$target" ) ]]; then
      echo "desktop-local evidence target must be a regular non-symlink: $target" >&2
      return 1
    fi
  done
  for name in desktop-registry.json desktop-environment.json process.json irrlicht-session.json transcript.jsonl; do
    source="$source_dir/$name"
    target="$destination/$name"
    if [[ ! -f "$source" || -L "$source" ]]; then
      echo "desktop-local evidence must be a regular non-symlink: $source" >&2
      return 1
    fi
    cp "$source" "$target" || return 1
  done
  if [[ ! -f "$recording" || -L "$recording" ]]; then
    echo "desktop-local recording must be a regular non-symlink: $recording" >&2
    return 1
  fi
  : > "$destination/hooks.jsonl" || return 1
  local line
  while IFS= read -r line; do
    if ! jq -e . >/dev/null 2>&1 <<<"$line"; then
      echo "desktop-local recording contains invalid JSONL" >&2
      return 1
    fi
    if jq -e --arg sid "$session_id" \
      '.kind == "hook_received" and .session_id == $sid and
       (.hook_name | type == "string" and length > 0)' \
      >/dev/null <<<"$line"; then
      printf '%s\n' "$line" >> "$destination/hooks.jsonl"
    fi
  done < "$recording"
  [[ -s "$destination/hooks.jsonl" ]] || {
    echo "desktop-local evidence has no hook_received event for $session_id" >&2
    return 1
  }
  desktop_validate_staged_evidence "$destination" "$session_id" "$workspace"
}

desktop_validate_staged_evidence() {
  local dir="$1" session_id="$2" workspace="$3"
  local local_id registry_pid irrlicht_pid
  jq -e --arg sid "$session_id" --arg cwd "$workspace" '
    (.sessionId | type == "string" and startswith("local_")) and
    .cliSessionId == $sid and .cwd == $cwd and
    ((has("envScopeId") | not) or .envScopeId == null)
  ' "$dir/desktop-registry.json" >/dev/null || return 1
  local_id="$(jq -r '.sessionId' "$dir/desktop-registry.json")"
  [[ "$local_id" == local_* ]] || return 1
  jq -e --arg cwd "$workspace" '
    .selected_environment == "Local" and .requested_workspace == $cwd
  ' "$dir/desktop-environment.json" >/dev/null || return 1
  jq -s -e --arg sid "$session_id" --arg cwd "$workspace" '
    length > 0 and
    ([.[] | .sessionId? // empty] | length > 0) and
    ([.[] | .sessionId? // empty] | unique) == [$sid] and
    ([.[] | .cwd? // empty] | length > 0) and
    ([.[] | .cwd? // empty] | unique) == [$cwd] and
    ([.[] | .entrypoint? // empty] | length > 0) and
    ([.[] | .entrypoint? // empty] | unique) == ["claude-desktop"]
  ' "$dir/transcript.jsonl" >/dev/null || return 1
  jq -s -e --arg sid "$session_id" '
    length > 0 and all(.[];
      .session_id == $sid and .kind == "hook_received" and
      (.hook_name | type == "string" and length > 0))
  ' "$dir/hooks.jsonl" >/dev/null || return 1
  registry_pid="$(jq -r 'if (.pid | type) == "number" and .pid > 0 and (.command | type) == "string" and (.command | length) > 0 then .pid else empty end' "$dir/process.json")"
  irrlicht_pid="$(jq -r --arg sid "$session_id" --arg cwd "$workspace" '
    if .session_id == $sid and .cwd == $cwd and
       (.pid | type) == "number" and .pid > 0 and
       .launcher.host_bundle_id == "com.anthropic.claudefordesktop"
    then .pid else empty end
  ' "$dir/irrlicht-session.json")"
  [[ -n "$registry_pid" && "$registry_pid" == "$irrlicht_pid" ]] || return 1
  return 0
}

desktop_write_execution_results() {
  local path="$1" scenario="$2" recording="$3" outcome="${4:-observed-passing}" reason="${5:-}"
  case "$outcome" in
    observed-passing) ;;
    observed-failure) [[ -n "$reason" ]] || return 1 ;;
    *) return 1 ;;
  esac
  jq -n \
    --arg scenario_id "$scenario" \
    --arg outcome "$outcome" \
    --arg recording "$recording" \
    --arg reason "$reason" \
    '{schema_version: 1,
      results: [{
        scenario_id: $scenario_id,
        execution_profile: "desktop-local",
        outcome: $outcome,
        recording: $recording,
        evidence: {
          desktop_registry: "desktop-registry.json",
          transcript: "transcript.jsonl",
          hooks: "hooks.jsonl",
          process: "process.json",
          irrlicht_session: "irrlicht-session.json",
          environment: "desktop-environment.json"
        }
      } + (if $reason == "" then {} else {reason: $reason} end)]}' > "$path"
}
