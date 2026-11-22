#!/usr/bin/env bash
# pool.sh — run coding agents one per isolated container or micro-VM, and show
# them in Irrlicht through its worker list (site/docs/worker-list.html).
#
#   pool.sh run <team> <task> [options] [-- <command> [args...]]
#   pool.sh answer <team> <task>      answer the stub agent's question
#   pool.sh supervise [--once] [--interval S]
#   pool.sh status                    print the worker list without writing it
#   pool.sh rm <team> <task>          stop and forget one task
#   pool.sh down                      stop and forget every task
#
# Options for run:
#   --backend docker|container   $VM_POOL_BACKEND, else docker. `container` is
#                                Apple's container CLI: one micro-VM per task.
#   --image IMAGE                $VM_POOL_IMAGE, else alpine:latest
#   --env NAME                   pass NAME's value from this environment into
#                                the guest (repeatable), e.g. a credential
#   --work-item TEXT             the row's work item (default: the task name)
#   -- command...                the task (default: guest/stub-agent)
#
# What crosses the boundary. The task's directory under $VM_POOL_DIR is
# mounted read-write at /irrlicht-state; guest/ is mounted read-only at
# /opt/irrlicht-guest. The guest writes four one-line files there — state,
# heartbeat, ended, exit — and this script reads nothing else. No transcript,
# prompt or output is read or forwarded.
#
# Environment: VM_POOL_DIR (default ~/.local/state/irrlicht-vm-pool),
# VM_POOL_NAME (the group header, default "VM pool"), IRRLICHT_HEARTBEAT_SECONDS
# (guest heartbeat, default 20), and the worker list's own
# IRRLICHT_WORKERLIST_FILE / IRRLICHT_HOME, resolved the way the daemon does.
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
GUEST="$HERE/guest"
POOL_DIR="${VM_POOL_DIR:-$HOME/.local/state/irrlicht-vm-pool}"
POOL_NAME="${VM_POOL_NAME:-VM pool}"
HEARTBEAT_SECONDS="${IRRLICHT_HEARTBEAT_SECONDS:-20}"
OUT="${IRRLICHT_WORKERLIST_FILE:-${IRRLICHT_HOME:-$HOME/.local/share/irrlicht}/workerlist.json}"
DOCKER="${VM_POOL_DOCKER:-docker}"
CONTAINER="${VM_POOL_CONTAINER_CLI:-container}"
# Marks a worker list this script wrote, so `down` removes only its own file.
# Unknown top-level fields are ignored by the daemon.
WRITER="irrlicht examples/vm-pool"

die() { echo "pool.sh: $*" >&2; exit 1; }

usage() { sed -n '2,10p' "$0" | sed 's/^# \{0,1\}//'; exit 2; }

valid_name() {
  [[ "$1" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]] || die "invalid name '$1' (letters, digits, . _ -)"
}

container_name() { printf 'irrlicht-pool-%s-%s' "$1" "$2"; }

# read_line <file> <default> prints the file's first line, or the default.
read_line() {
  local line
  if [[ -f "$1" ]] && IFS= read -r line < "$1"; then
    printf '%s' "$line"
  else
    printf '%s' "$2"
  fi
}

# backend_status <backend> <name> prints running, stopped or missing.
backend_status() {
  local out
  case "$1" in
    docker)
      out=$("$DOCKER" inspect -f '{{.State.Running}}' "$2" 2>/dev/null) || { echo missing; return; }
      [[ "$out" == true ]] && echo running || echo stopped
      ;;
    container)
      # Not yet run against Apple's container CLI: the `.[0].status` field
      # is the intended reading of `container inspect`, unverified.
      out=$("$CONTAINER" inspect "$2" 2>/dev/null) || { echo missing; return; }
      [[ "$(jq -r '.[0].status // empty' <<<"$out")" == running ]] && echo running || echo stopped
      ;;
    *) die "unknown backend '$1'" ;;
  esac
}

# backend_last_run <backend> <name> prints "<end> <exit>" for a stopped
# container, read from the backend itself — the fallback for a guest that was
# killed before its entrypoint could write `ended` and `exit`. Prints nothing
# when the backend cannot tell.
backend_last_run() {
  local out
  case "$1" in
    docker)
      out=$("$DOCKER" inspect -f '{{.State.FinishedAt}} {{.State.ExitCode}}' "$2" 2>/dev/null) || return 0
      # FinishedAt carries nanoseconds; the worker list takes whole seconds.
      sed -E 's/^([0-9-]+T[0-9:]+)(\.[0-9]+)?Z /\1Z /' <<<"$out"
      ;;
  esac
}

backend_remove() {
  case "$1" in
    docker) "$DOCKER" rm -f "$2" >/dev/null 2>&1 || true ;;
    container)
      "$CONTAINER" stop "$2" >/dev/null 2>&1 || true
      "$CONTAINER" delete "$2" >/dev/null 2>&1 || true
      ;;
  esac
}

cmd_run() {
  [[ $# -ge 2 ]] || usage
  local team="$1" task="$2"
  shift 2
  valid_name "$team"
  valid_name "$task"
  local backend="${VM_POOL_BACKEND:-docker}" image="${VM_POOL_IMAGE:-alpine:latest}" item="$task"
  local envs=() cmd=()
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --backend) backend="${2:?--backend needs a value}"; shift 2 ;;
      --image) image="${2:?--image needs a value}"; shift 2 ;;
      --env) envs+=(-e "${2:?--env needs a name}"); shift 2 ;;
      --work-item) item="${2:?--work-item needs a value}"; shift 2 ;;
      --) shift; cmd=("$@"); break ;;
      *) die "unknown option '$1'" ;;
    esac
  done
  [[ ${#cmd[@]} -gt 0 ]] || cmd=(/opt/irrlicht-guest/stub-agent)
  case "$backend" in docker | container) ;; *) die "unknown backend '$backend'" ;; esac

  local dir="$POOL_DIR/$team/$task" name
  name=$(container_name "$team" "$task")
  [[ "$(backend_status "$backend" "$name")" == missing ]] || die "$team/$task is already running or not removed (pool.sh rm $team $task)"
  rm -rf "$dir"
  mkdir -p "$dir"
  printf '%s\n' "$backend" > "$dir/backend"
  printf '%s\n' "$item" > "$dir/work_item"
  # The slot reads busy and fresh while the image starts; the guest takes
  # over both files once its entrypoint runs.
  printf 'busy\n' > "$dir/state"
  date -u +%Y-%m-%dT%H:%M:%SZ > "$dir/heartbeat"

  local cli="$DOCKER" ro=":ro"
  # Apple's container CLI takes -v host:guest; it is mounted read-write there.
  [[ "$backend" == container ]] && { cli="$CONTAINER"; ro=""; }
  "$cli" run -d --name "$name" \
    -e "IRRLICHT_HEARTBEAT_SECONDS=$HEARTBEAT_SECONDS" \
    ${envs[@]+"${envs[@]}"} \
    -v "$dir:/irrlicht-state" \
    -v "$GUEST:/opt/irrlicht-guest$ro" \
    "$image" /opt/irrlicht-guest/entrypoint "${cmd[@]}" >/dev/null
  echo "started $team/$task ($backend, $name)"
}

cmd_answer() {
  [[ $# -eq 2 ]] || usage
  valid_name "$1"
  valid_name "$2"
  local dir="$POOL_DIR/$1/$2"
  [[ -d "$dir" ]] || die "no task $1/$2"
  : > "$dir/answer"
  echo "answered $1/$2"
}

# worker_json <team> <task> prints one worker of the status file. A running
# task is busy (and waiting while its guest says so); a task that stopped is
# free, with its last run once the guest recorded one.
worker_json() {
  local dir="$POOL_DIR/$1/$2" backend name status ended exit_code fallback
  backend=$(read_line "$dir/backend" docker)
  name=$(container_name "$1" "$2")
  status=$(backend_status "$backend" "$name")
  ended=$(read_line "$dir/ended" "")
  exit_code=$(read_line "$dir/exit" "")
  if [[ "$status" == stopped && -z "$ended" ]]; then
    fallback=$(backend_last_run "$backend" "$name")
    ended=${fallback%% *}
    exit_code=${fallback##* }
  fi
  jq -nc \
    --arg name "$2" \
    --arg item "$(read_line "$dir/work_item" "$2")" \
    --arg status "$status" \
    --arg state "$(read_line "$dir/state" busy)" \
    --arg heartbeat "$(read_line "$dir/heartbeat" "")" \
    --arg ended "$ended" \
    --arg exit "$exit_code" '
    # Only a well-formed UTC timestamp is passed on: one malformed value
    # would make the whole file unreadable for every team.
    def ts: if test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$") then . else empty end;
    {name: $name, busy: ($status == "running"), work_item: $item}
    + (if $status == "running" and $state == "waiting" then {state: "waiting"} else {} end)
    + (if $status == "running" then ($heartbeat | ts | {last_heartbeat: .}) // {} else {} end)
    + (if $status != "running" then
         ($ended | ts | {last_run: {end: ., exit: ($exit | tonumber? // null)}}) // {}
       else {} end)'
}

# build_document prints the whole worker list. Each team is stamped now: this
# script is every team's writer, so a stopped supervisor turns them stale.
build_document() {
  local now teams="[]" teamdir taskdir team workers
  now=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  for teamdir in "$POOL_DIR"/*/; do
    [[ -d "$teamdir" ]] || continue
    team=$(basename "$teamdir")
    workers="[]"
    for taskdir in "$teamdir"*/; do
      [[ -d "$taskdir" ]] || continue
      workers=$(jq -c --argjson w "$(worker_json "$team" "$(basename "$taskdir")")" '. + [$w]' <<<"$workers")
    done
    teams=$(jq -c --arg name "$team" --arg now "$now" --argjson workers "$workers" \
      '. + [{name: $name, updated: $now, workers: $workers}]' <<<"$teams")
  done
  jq -n --arg name "$POOL_NAME" --arg writer "$WRITER" --argjson hb "$HEARTBEAT_SECONDS" --argjson teams "$teams" \
    '{schema: 1, name: $name, writer: $writer, heartbeat_interval_seconds: $hb, teams: $teams}'
}

# write_document replaces the worker list atomically, as schema v1 asks.
write_document() {
  local tmp
  mkdir -p "$(dirname "$OUT")"
  tmp=$(mktemp "$(dirname "$OUT")/.workerlist.XXXXXX")
  if ! build_document > "$tmp"; then
    rm -f "$tmp"
    die "could not build the worker list"
  fi
  mv "$tmp" "$OUT"
}

cmd_supervise() {
  local once=false interval=5
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --once) once=true; shift ;;
      --interval) interval="${2:?--interval needs seconds}"; shift 2 ;;
      *) die "unknown option '$1'" ;;
    esac
  done
  while :; do
    write_document
    $once && return 0
    sleep "$interval"
  done
}

cmd_rm() {
  [[ $# -eq 2 ]] || usage
  valid_name "$1"
  valid_name "$2"
  local dir="$POOL_DIR/$1/$2"
  backend_remove "$(read_line "$dir/backend" docker)" "$(container_name "$1" "$2")"
  rm -rf "$dir"
  rmdir "$POOL_DIR/$1" 2>/dev/null || true
  echo "removed $1/$2"
}

cmd_down() {
  local teamdir taskdir
  for teamdir in "$POOL_DIR"/*/; do
    [[ -d "$teamdir" ]] || continue
    for taskdir in "$teamdir"*/; do
      [[ -d "$taskdir" ]] || continue
      cmd_rm "$(basename "$teamdir")" "$(basename "$taskdir")"
    done
  done
  if [[ -f "$OUT" ]] && [[ "$(jq -r '.writer // empty' "$OUT" 2>/dev/null)" == "$WRITER" ]]; then
    rm -f "$OUT"
    echo "removed $OUT"
  fi
}

command -v jq >/dev/null 2>&1 || die "jq is required"
[[ $# -ge 1 ]] || usage
sub="$1"
shift
case "$sub" in
  run) cmd_run "$@" ;;
  answer) cmd_answer "$@" ;;
  supervise) cmd_supervise "$@" ;;
  status) build_document ;;
  rm) cmd_rm "$@" ;;
  down) cmd_down ;;
  -h | --help | help) usage ;;
  *) die "unknown command '$sub'" ;;
esac
