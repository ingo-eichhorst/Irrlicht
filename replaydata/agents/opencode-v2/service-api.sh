#!/usr/bin/env bash
# service-api.sh — the opencode-v2 driver's dealings with a running OpenCode 2.x
# service, sourced by driver-interactive.sh beside it and by its tests
# (tools/onboarding-factory/scripts/lib/opencode-v2-driver_test.sh) and the
# export's Go test (cmd/replay/opencode_v2_export_test.go). Functions only.
#
# THE SERVICE IS NOT OURS. One `opencode serve --service` hosts every 2.x
# session of its user, on a fixed port; the rig drives it over HTTP and never
# starts, stops or reconfigures it. Everything below either reads (the
# service's service.json, its environment, its log, its store — the store
# read-only) or asks the service's API to act on a session the driver created.
#
# THE PASSWORD. $XDG_STATE_HOME/opencode/service.json carries the service's
# password, and it is the only way to authenticate to the API. It is read into
# OC2_PW — a plain, unexported shell variable — and reaches curl only on
# STDIN, as a `-K -` config line written by the printf builtin: never argv
# (where `ps` shows it), never the environment, never a file, never a log. The
# daemon never opens that file at all; righome's
# TestTheOpenCodeDaemonNeverNamesTheServiceSecret holds it to that.

# oc2_now prints the current epoch second.
oc2_now() { date +%s; }

# oc2_refuse <message...> reports a refusal on stderr and returns 1. A refusal
# means the run cannot be recorded faithfully, so the caller exits.
oc2_refuse() {
  local line
  for line in "$@"; do echo "[driver] REFUSING: $line" >&2; done
  return 1
}

# oc2_proc_environ <pid> prints a process's environment, one NAME=value per
# line. Linux reads /proc; macOS asks `ps -E`, which prints the command line
# followed by NAME=value words (a value with a space in it is read only to the
# space — a mismatch, never a false match, because every caller compares for
# equality and refuses otherwise). Read once per run: oc2_env_value then picks
# names out of the text.
oc2_proc_environ() {
  local pid="$1"
  if [[ -r "/proc/$pid/environ" ]]; then
    tr '\0' '\n' < "/proc/$pid/environ"
    return 0
  fi
  ps -E -ww -o command= -p "$pid" 2>/dev/null | tr ' ' '\n'
  return 0
}

# oc2_env_value <environ-text> <var> prints one variable from
# oc2_proc_environ's output, or nothing when it is not there.
oc2_env_value() {
  sed -n "s/^$2=//p" <<<"$1" | head -n1
  return 0
}

# oc2_is_service_args <args...> is the shell form of the daemon's own service
# test, opencode.ServicePIDs (core/adapters/inbound/agents/opencode/pid.go):
# argv0 matches processCmdPattern (an opencode, opencode2 or opencode.exe
# binary), and IsServiceArgv (serviceargv.go) holds — the first positional
# after argv0 is `serve`, and `--service` follows it before any `--`. The
# options whose next token is a value are serviceargv.go's valueOptions.
oc2_is_service_args() {
  local argv0="${1:-}"; shift || true
  case "${argv0##*/}" in opencode|opencode2|opencode.exe) ;; *) return 1 ;; esac
  local sub="" a skip=0
  while [[ $# -gt 0 ]]; do
    a="$1"; shift
    if (( skip )); then skip=0; continue; fi
    case "$a" in
      --) return 1 ;;
      --server|--session|-s|--prompt|--log-level|--completions) skip=1 ;;
      -*) ;;
      *) sub="$a"; break ;;
    esac
  done
  [[ "$sub" == "serve" ]] || return 1
  for a in "$@"; do
    case "$a" in --) return 1 ;; --service) return 0 ;; esac
  done
  return 1
}

# oc2_service_pids prints the pid of every running opencode 2.x service, one
# per line.
oc2_service_pids() {
  local pid args
  while read -r pid args; do
    [[ "$pid" =~ ^[0-9]+$ ]] || continue
    # shellcheck disable=SC2086  # word-splitting the argv string is the point
    oc2_is_service_args $args && printf '%s\n' "$pid"
  done < <(ps -axo pid=,args= 2>/dev/null)
  return 0
}

# oc2_preflight checks everything the run depends on and that the rig must
# not change, then loads OC2_URL, OC2_SVC_PID and OC2_PW. It refuses (returns
# 1, saying why) when:
#   - OPENCODE_DB is set — the service runs without it, so the daemon would
#     watch a different database than the service writes;
#   - a variable in SERVICE_ENV_VARS is unset, relative, or differs from the
#     SERVICE's own value — the daemon would watch one tree, the service write
#     another;
#   - $XDG_STATE_HOME/opencode/service.json is missing or names no pid/url;
#   - that pid is dead, or its argv is not `serve --service`;
#   - not exactly one `serve --service` runs (pid.go's servicePIDFor binds
#     nothing otherwise, so every session would be recorded PID-less);
#   - the address the service's beacon resolves — the plugin runs
#     `irrlichd hook-post` INSIDE the service — is not this run's daemon.
oc2_preflight() {
  local v ours theirs sj pid url args pids n port beacon_bind beacon_home addr_file addr svc_env
  if [[ -n "${OPENCODE_DB:-}" ]]; then
    oc2_refuse "OPENCODE_DB is set ($OPENCODE_DB); the 2.x service runs without it, so the" \
      "daemon would watch a different database than the service writes. Unset it."
    return 1
  fi
  for v in $SERVICE_ENV_VARS; do
    ours="${!v:-}"
    if [[ "$ours" != /* ]]; then
      oc2_refuse "$v must be exported as an absolute path (the service's own value; see" \
        "replaydata/agents/opencode-v2/prerequisites.md), got '${ours}'"
      return 1
    fi
  done

  sj="$XDG_STATE_HOME/opencode/service.json"
  if [[ ! -r "$sj" ]]; then
    oc2_refuse "no readable $sj — no 2.x service runs under this XDG_STATE_HOME, and the" \
      "rig never starts one"
    return 1
  fi
  pid="$(jq -r '.pid // empty' "$sj" 2>/dev/null || true)"
  url="$(jq -r '.url // empty' "$sj" 2>/dev/null || true)"
  if [[ ! "$pid" =~ ^[0-9]+$ ]] || [[ ! "$url" =~ ^http://(127\.0\.0\.1|localhost):[0-9]+$ ]]; then
    oc2_refuse "$sj names no usable pid/url (pid='$pid', url='$url')"
    return 1
  fi
  if ! kill -0 "$pid" 2>/dev/null; then
    oc2_refuse "the service pid $pid in $sj is not running — the rig never starts one"
    return 1
  fi
  args="$(ps -o args= -p "$pid" 2>/dev/null || true)"
  # shellcheck disable=SC2086  # word-splitting the argv string is the point
  if ! oc2_is_service_args $args; then
    oc2_refuse "pid $pid from $sj is not an opencode 'serve --service' (args: $args)"
    return 1
  fi
  pids="$(oc2_service_pids | sort -u)"
  n="$(printf '%s\n' "$pids" | grep -c .)"
  if [[ "$n" != "1" ]]; then
    oc2_refuse "$n opencode 'serve --service' processes run ($(printf '%s' "$pids" | tr '\n' ' '))." \
      "The daemon binds a service-hosted session only while exactly one runs" \
      "(opencode/pid.go servicePIDFor), so every session would record PID-less."
    return 1
  fi
  if [[ "$pids" != "$pid" ]]; then
    oc2_refuse "the one running service is pid $pids, but $sj names $pid"
    return 1
  fi

  svc_env="$(oc2_proc_environ "$pid")"
  for v in $SERVICE_ENV_VARS; do
    ours="${!v}"
    theirs="$(oc2_env_value "$svc_env" "$v")"
    if [[ "$theirs" != "$ours" ]]; then
      oc2_refuse "the service (pid $pid) runs with $v='${theirs}', this run exports '$ours' —" \
        "the daemon would watch a different tree than the service writes"
      return 1
    fi
  done
  theirs="$(oc2_env_value "$svc_env" OPENCODE_DB)"
  if [[ -n "$theirs" ]]; then
    oc2_refuse "the service runs with OPENCODE_DB=$theirs, which this rig's store reads do not follow"
    return 1
  fi

  # Where the plugin's beacon posts: core/pkg/daemonaddr's client ladder, run
  # over the SERVICE's environment — IRRLICHT_BIND_ADDR, else
  # $IRRLICHT_HOME/irrlichd.addr, else $HOME/.local/share/irrlicht/irrlichd.addr.
  port="${IRRLICHT_BIND_ADDR##*:}"
  if [[ ! "$port" =~ ^[0-9]+$ ]]; then
    oc2_refuse "IRRLICHT_BIND_ADDR ('${IRRLICHT_BIND_ADDR:-}') names no daemon port to check the beacon against"
    return 1
  fi
  beacon_bind="$(oc2_env_value "$svc_env" IRRLICHT_BIND_ADDR)"
  if [[ -n "$beacon_bind" ]]; then
    addr="$beacon_bind"
    addr_file="(the service's IRRLICHT_BIND_ADDR)"
  else
    beacon_home="$(oc2_env_value "$svc_env" IRRLICHT_HOME)"
    if [[ -n "$beacon_home" ]]; then
      addr_file="$beacon_home/irrlichd.addr"
    else
      addr_file="$(oc2_env_value "$svc_env" HOME)/.local/share/irrlicht/irrlichd.addr"
    fi
    addr="$(head -c 64 "$addr_file" 2>/dev/null | tr -d '[:space:]' || true)"
  fi
  if [[ "${addr##*:}" != "$port" ]]; then
    oc2_refuse "the plugin's beacon runs inside the service and resolves the daemon from" \
      "$addr_file, which names '${addr}', not this run's daemon on port $port. Record in" \
      "coexist mode with IRRLICHT_ONBOARD_HOME=<the service's HOME>/.local/share/irrlicht," \
      "or every hook goes to another daemon and the recording comes back hook-free."
    return 1
  fi

  # shellcheck disable=SC2034  # read by driver-interactive.sh, which sources this file
  OC2_SVC_PID="$pid"
  OC2_URL="$url"
  # Read into a plain, unexported variable — see the header.
  OC2_PW="$(jq -r '.password // empty' "$sj" 2>/dev/null || true)"
  if [[ -z "$OC2_PW" ]]; then
    oc2_refuse "$sj carries no password, so the API cannot be authenticated to"
    return 1
  fi
  return 0
}

# oc2_api <method> <path> [<json-body>] calls the service API and prints the
# response body. Every request carries a deadline (-m): the smaller of 20s
# and what is left of the run, never less than 1. The credential travels on
# stdin only (see the header). A non-2xx answer or a transport failure returns
# 1 with curl's reason on stderr.
oc2_api() {
  local method="$1" path="$2" body="${3:-}" left max pw
  left=$(( ${OC2_DEADLINE:-$(( $(oc2_now) + 20 ))} - $(oc2_now) ))
  max=20; (( left < max )) && max=$left; (( max < 1 )) && max=1
  pw="${OC2_PW//\\/\\\\}"; pw="${pw//\"/\\\"}"
  local -a args=(-sS -m "$max" --fail-with-body -X "$method" -H 'content-type: application/json' -K -)
  [[ -n "$body" ]] && args+=(--data-binary "$body")
  printf 'user = "opencode:%s"\n' "$pw" | curl "${args[@]}" "$OC2_URL$path"
}

# oc2_store prints the 2.x store the daemon watches: $XDG_DATA_HOME/opencode/
# opencode.db (opencode.StorePath with OPENCODE_DB unset, which preflight
# requires).
oc2_store() { printf '%s\n' "$XDG_DATA_HOME/opencode/opencode.db"; }

# oc2_valid_sid <sid> accepts only an opencode session id, so it can be put
# inside an SQL literal and a URL path without quoting.
oc2_valid_sid() { [[ "${1:-}" =~ ^ses_[A-Za-z0-9]+$ ]]; }

# oc2_idle_mark <db> <sid> prints "<time_idle>|<idle_outcome>" for a session —
# 0 and empty while it has never gone idle. Read-only. A failed read prints
# nothing and returns 1: "could not look" is never "not idle yet".
oc2_idle_mark() {
  local db="$1" sid="$2" out
  oc2_valid_sid "$sid" || return 1
  out="$(sqlite3 -readonly -cmd '.timeout 5000' "$db" \
    "SELECT coalesce(time_idle,0)||'|'||coalesce(idle_outcome,'') FROM session_v2 WHERE id='$sid'" 2>&1)" || {
    echo "[driver] reading $sid's idle state from $db failed: $out" >&2
    return 1
  }
  [[ "$out" =~ ^[0-9]+\| ]] || { echo "[driver] no session_v2 row for $sid in $db (got '$out')" >&2; return 1; }
  printf '%s\n' "$out"
}

# oc2_export_session <db> <sid> <out> writes one line per session_message row
# of the session, in seq order: the row's data JSON plus its `type` column and
# the `_cwd`/`_ts` keys — exactly the map the daemon's own reader hands
# opencode.Parser (metrics_v2.go buildV2MessageRaw: type, _cwd from
# session_v2.directory, _ts from session_message.time_updated). The parser
# picks its 2.x branch from `type` (parser_v2.go: user, assistant, idle — no
# 1.x part type uses those), so the replay needs no second parser. The store
# is opened read-only. Assistant rows are rewritten in place while a step
# streams, so this is the final snapshot of each row.
oc2_export_session() {
  local db="$1" sid="$2" out="$3"
  oc2_valid_sid "$sid" || { echo "[driver] refusing to export '$sid': not a session id" >&2; return 1; }
  sqlite3 -readonly -cmd '.timeout 5000' -cmd '.mode list' -cmd '.separator ""' "$db" \
    "SELECT json_set(m.data, '\$.type', m.type, '\$._cwd', s.directory, '\$._ts', m.time_updated)
       FROM session_message m JOIN session_v2 s ON s.id = m.session_id
      WHERE m.session_id = '$sid'
      ORDER BY m.seq;" > "$out"
}

# oc2_plugin_loaded <log> <mark-byte> <plugin-path> reports whether the
# service's log, past byte <mark-byte>, shows the plugin at <plugin-path>
# loading with NO "failed to load plugin" for it. The service logs
# `msg="loading plugin" id=<path>` for every load ATTEMPT, a failing one
# included — a broken artifact is followed within milliseconds by
# `message="failed to load plugin" target=<path>` (measured in the sandbox
# service's log: the 1.x artifact under 2.1.4). So a load line alone is not
# readiness; a load line with no failure since the mark is.
oc2_plugin_loaded() {
  local log="$1" mark="$2" plugin="$3" tail_lines
  # A byte offset, so tail seeks rather than counting the shared, ever-growing
  # service log's lines from the start on every poll.
  tail_lines="$(tail -c +"$((mark + 1))" "$log" 2>/dev/null || true)"
  grep -qF "failed to load plugin\" target=$plugin " <<<"$tail_lines" && return 2
  grep -qF "msg=\"loading plugin\" id=$plugin " <<<"$tail_lines"
}
