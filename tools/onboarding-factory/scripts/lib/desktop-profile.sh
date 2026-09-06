#!/usr/bin/env bash
# Claude Desktop Local profile guards and evidence staging.

# The recipe lint below reads the cell's recipe through the catalog, which has
# ONE reader in this tree. Sourced the way recipe-lint.sh sources it; sourcing
# it twice (run-cell.sh already does) only redefines its functions.
# shellcheck source=shard-lib.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/shard-lib.sh"

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

# desktop_daemon_home <repo-root> prints the recording daemon's IRRLICHT_HOME
# for a desktop-local run. It is deliberately SHORT and outside the clone.
#
# The daemon binds $IRRLICHT_HOME/irrlichd.sock, and macOS refuses an AF_UNIX
# path over 103 bytes (spawn-record-daemon.sh's RECORD_DAEMON_SOCK_MAX carries
# the measurement). A staging-relative home measures 114 bytes in a bare clone
# and 158 in a linked worktree, so every desktop-local run used to die inside
# setupUnixSocket before the driver ever ran.
#
# The suffix keys on the clone, so two checkouts never share one home and the
# path stays stable across runs of the same clone — a crashed run's stale
# socket is cleared by the next one rather than accumulating scratch dirs.
desktop_daemon_home() {
  local repo_root="$1" common_dir clone_root key
  common_dir="$(git -C "$repo_root" rev-parse --path-format=absolute --git-common-dir 2>/dev/null)" || {
    echo "desktop-local cannot resolve the shared Git directory" >&2
    return 1
  }
  clone_root="$(cd "$(dirname "$common_dir")" && pwd -P)" || return 1
  key="$(printf '%s' "$clone_root" | cksum | cut -d' ' -f1)" || return 1
  [[ "$key" =~ ^[0-9]+$ ]] || {
    echo "desktop-local could not derive a daemon-home key for $clone_root" >&2
    return 1
  }
  printf '/tmp/irr-onb-%s\n' "$key"
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
  # A `script` recipe is no longer refused outright (#1888) — which step types
  # the Desktop driver can drive is decided by desktop_recipe_gaps below, from
  # the driver's own declaration, rather than by a blanket "prompt only".
  if [[ "$(jq -c '.settings // {}' <<<"$cell_json")" != "{}" ]] ||
     [[ "$(jq -c '.env // {}' <<<"$cell_json")" != "{}" ]] ||
     [[ "$(jq -c '.mock // empty' <<<"$cell_json")" != "" ]] ||
     [[ "$(jq -r '.bare_mode // false' <<<"$cell_json")" == "true" ]]; then
    echo "desktop-local refuses settings, env, mock, and bare-mode changes" >&2
    return 1
  fi
  return 0
}

# --- Recipe → Desktop control lint (#1888) ---------------------------------
#
# The Desktop driver's grammar is owned by the Go binary
# (tools/onboarding-factory/internal/desktopdriver/recipe.go). driver-desktop.sh
# carries the scraped declaration these functions read, and the Go package's
# contract_test.go fails when the two disagree — so this reads ONE grammar, not
# a second hand-kept one.
#
# THE REFUSAL MUST HAPPEN HERE, before precheck, before the managed-file
# snapshot, before the daemon and before any deep link. A Desktop-undrivable
# step discovered mid-run has already cost a live Claude Desktop session and
# rewritten the operator's real agent configuration files.

# desktop_driver_declaration <driver-file> <NAME>
#   → the value of the driver's top-level `NAME=` constant, quotes and a
#     trailing comment stripped, whitespace collapsed. Empty when absent.
#     Same sed-scrape shape recipe-lint.sh uses for DRIVE_ELICITS.
desktop_driver_declaration() {
  local file="$1" name="$2" raw
  [[ -f "$file" ]] || return 0
  raw="$(sed -n "s/^${name}=//p" "$file" | head -1)"
  [[ -n "$raw" ]] || return 0
  raw="${raw%%#*}"
  raw="${raw//\"/}"
  raw="${raw//\'/}"
  # Unquoted expansion collapses whitespace and drops leading/trailing blanks.
  # shellcheck disable=SC2086  # deliberate word splitting: this is a token list
  echo $raw
}

# desktop_missing_control_for <driver-file> <step-type>
#   → the control the Desktop driver lacks for this step type, or empty when the
#     driver does not declare one.
desktop_missing_control_for() {
  local file="$1" want="$2" pair
  for pair in $(desktop_driver_declaration "$file" DRIVE_MISSING_CONTROLS); do
    [[ "${pair%%:*}" == "$want" ]] && { echo "${pair#*:}"; return 0; }
  done
  return 0
}

# desktop_recipe_gaps <driver-file> <coverage-id> <adapter>
#   → one `<step-type>\t<control>` line per recipe step type the Desktop driver
#     cannot elicit. Returns 0 when the recipe is runnable, 1 when it is not,
#     and 2 when the check itself could not run.
#
#   Exit 2 is the case that matters most: a driver with no DRIVE_ELICITS scrapes
#   to an empty set, which would make EVERY step look like a gap OR — read the
#   other way round — make a caller that ignores the empty case report "no gaps"
#   for a driver that declares nothing. Neither reading may be silent.
desktop_recipe_gaps() {
  local driver="$1" coverage_id="$2" adapter="$3"
  local elicits needed step control
  if [[ ! -f "$driver" ]]; then
    echo "desktop-local recipe lint: driver not found: $driver" >&2
    return 2
  fi
  elicits="$(desktop_driver_declaration "$driver" DRIVE_ELICITS)"
  if [[ -z "$elicits" ]]; then
    echo "desktop-local recipe lint: $(basename "$driver") declares no DRIVE_ELICITS — this check cannot run, which is a failure, not a pass" >&2
    return 2
  fi
  needed="$(shard_recipe "$coverage_id" "$adapter" | jq -r '.script // [] | .[].type' 2>/dev/null | sort -u)"
  # A cell with no script (a plain prompt cell) has nothing to lint.
  [[ -n "$needed" ]] || return 0
  local gaps=0
  while IFS= read -r step; do
    [[ -n "$step" ]] || continue
    case " $elicits " in
      *" $step "*) continue ;;
    esac
    control="$(desktop_missing_control_for "$driver" "$step")"
    [[ -n "$control" ]] || control="unknown"
    printf '%s\t%s\n' "$step" "$control"
    gaps=1
  done <<<"$needed"
  [[ "$gaps" -eq 0 ]] || return 1
  return 0
}

# desktop_recipe_sessions <coverage-id> <adapter>
#   → how many live sessions the recipe asks for: 1, plus one per
#     `start_session` step. 0 when the cell carries no script.
desktop_recipe_sessions() {
  local coverage_id="$1" adapter="$2" script starts
  script="$(shard_recipe "$coverage_id" "$adapter" | jq -c '.script // empty' 2>/dev/null)"
  [[ -n "$script" ]] || { echo 0; return 0; }
  starts="$(jq -r '[.[] | select(.type == "start_session")] | length' <<<"$script" 2>/dev/null)"
  [[ "$starts" =~ ^[0-9]+$ ]] || {
    echo "desktop-local: cannot count start_session steps in $adapter/$coverage_id" >&2
    return 1
  }
  echo $(( starts + 1 ))
}

# desktop_recipe_rig_gaps <coverage-id> <adapter>
#   → 0 when this rig can stage the recipe's evidence, 1 + a reason otherwise.
#
#   The Go driver owns as many live Desktop sessions as a recipe asks for and
#   keeps their identities apart. This RIG does not: desktop_stage_evidence and
#   desktop_write_execution_results below both take ONE session id, one
#   transcript and one workspace. Refusing here is the difference between "the
#   recording covers one of two sessions" — which nothing downstream can see —
#   and a named refusal that costs no live run.
desktop_recipe_rig_gaps() {
  local coverage_id="$1" adapter="$2" sessions
  sessions="$(desktop_recipe_sessions "$coverage_id" "$adapter")" || return 1
  if [[ "$sessions" -gt 1 ]]; then
    echo "the recipe asks for $sessions live Desktop sessions; this recording rig stages evidence for one" >&2
    return 1
  fi
  return 0
}

# desktop_report_recipe_gaps <driver-file> <coverage-id> <adapter> <scenario>
#   Prints the `not runnable through Desktop` verdict and returns the exit
#   status run-cell.sh should use: 0 runnable, 4 not runnable, 2 cannot check.
desktop_report_recipe_gaps() {
  local driver="$1" coverage_id="$2" adapter="$3" scenario="$4" gaps status
  gaps="$(desktop_recipe_gaps "$driver" "$coverage_id" "$adapter")"
  status=$?
  case "$status" in
    0)
      if ! desktop_recipe_rig_gaps "$coverage_id" "$adapter"; then
        echo "not runnable through Desktop: $adapter/$scenario exceeds this recording rig's evidence staging." >&2
        return 4
      fi
      return 0
      ;;
    2) echo "desktop-local: refusing $adapter/$scenario — the recipe lint could not run." >&2; return 2 ;;
  esac
  {
    echo "not runnable through Desktop: $adapter/$scenario needs Desktop controls the driver cannot elicit:"
    while IFS=$'\t' read -r step control; do
      [[ -n "$step" ]] && printf '  - step type %s needs missing control: %s\n' "$step" "$control"
    done <<<"$gaps"
    echo "Nothing about Claude Desktop was changed. Record this cell with the cli-local profile,"
    echo "or measure the missing control against a fresh accessibility dump and teach the Go driver."
  } >&2
  return 4
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
  # lsof exits non-zero both for "found no listener" and for "could not look"
  # (missing binary, denied permission), so a bare non-zero must never be read
  # as "the port is free". Establish that the probe can see anything at all
  # first, using this process's own descriptors as the positive control.
  command -v lsof >/dev/null 2>&1 || {
    echo "desktop-local cannot check port $port: lsof is not on PATH" >&2
    return 1
  }
  if [[ -z "$(lsof -p "$$" 2>/dev/null)" ]]; then
    echo "desktop-local cannot check port $port: lsof reports nothing for this process" >&2
    return 1
  fi
  if [[ -n "$(lsof -nP -iTCP:"$port" -sTCP:LISTEN 2>/dev/null)" ]]; then
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
