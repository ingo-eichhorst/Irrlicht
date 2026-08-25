#!/usr/bin/env bash
# recipe-runtime.sh — the per-cell recipe's RUNTIME block: `bare_mode`, `env`
# and `mock`. Pure functions over a CELL_JSON blob; no I/O beyond reading a
# driver file. Sourced by run-cell.sh, unit-tested by recipe-runtime_test.sh.
#
# WHY THIS EXISTS (#1803). Before it, a scenario that needed the agent pointed
# at a local mock could not be expressed as a recipe at all. Three gaps
# compounded (all still readable in the tree's history):
#
#   1. lib/shard-lib.sh's `shard_cell` projected a FIXED field list out of
#      `.details.recipe`, so an `env` key added to a recipe was invisible to
#      run-cell.sh no matter what it said.
#   2. driver-interactive.sh's `tmux new-session` carried no `-e VAR=value`,
#      and a tmux pane inherits the tmux SERVER's environment, not the calling
#      shell's — so exporting a variable in run-cell.sh could never reach the
#      agent CLI.
#   3. claude prefers OAuth/keychain over ANTHROPIC_API_KEY unless it is run
#      with `--bare`, and no recipe field asked for that.
#
# The workaround was a bespoke recorder per scenario, and
# recording/record-token-quota-exhausted.sh's header names this file's job as
# the fix: "(1) add `bare_mode` + `env` to the per-cell … block, (2) patch
# … init_session to honor them …, (3) launch the mock binary as a pre-driver
# hook in run-cell.sh." Four more error scenarios is where a fourth bespoke
# script stopped being reasonable.
#
# THE REFUSALS ARE THE POINT, not defensive clutter. Every failure mode here is
# silent in the direction that matters: a recipe whose `env` never reaches the
# pane records a healthy-looking fixture against the REAL provider — real
# credentials, real tokens, and a green recording that proves nothing about the
# error path it was written for. So every one of these is a hard refusal with a
# named reason, per AGENTS.md's "a verification mechanism must fail loudly when
# it cannot run".
#
# Shell options: this file requires nothing of the caller's options and changes
# none of them. Every function RETURNS its verdict and prints its reason on
# stderr; none of them exits.

# ---------------------------------------------------------------------------
# Driver capability contract
# ---------------------------------------------------------------------------

# driver_runtime_supports <driver-file> <capability>
#   → 0 when the driver's top-level `DRIVE_SUPPORTS=` constant lists the
#     capability, 1 otherwise (including a missing file or missing constant).
#
#   Read from the driver's SOURCE by the same sed-scrape recipe-lint.sh uses
#   for DRIVE_ELICITS, and for the same reason: the grammar has ONE owner, in
#   the driver, rather than a parallel manifest that can drift from it.
#   Tolerant of single or double quotes and a trailing comment.
driver_runtime_supports() {
  local file="$1" want="$2" raw cap
  [[ -f "$file" ]] || return 1
  raw="$(sed -n 's/^DRIVE_SUPPORTS=//p' "$file" | head -1)"
  [[ -n "$raw" ]] || return 1
  raw="${raw%%#*}"
  raw="${raw//\"/}"
  raw="${raw//\'/}"
  for cap in $raw; do
    [[ "$cap" == "$want" ]] && return 0
  done
  return 1
}

# ---------------------------------------------------------------------------
# Reading the runtime block
# ---------------------------------------------------------------------------

# recipe_runtime_bare <cell-json> → "true" | "false"
#   Anything other than a literal JSON `true` is false, including absent.
recipe_runtime_bare() {
  local cell="$1" v
  v="$(jq -r 'if .bare_mode == true then "true" else "false" end' <<<"$cell" 2>/dev/null)"
  if [[ "$v" == "true" ]]; then echo true; else echo false; fi
  return 0
}

# recipe_runtime_mock_port <cell-json> → the mock's port, or empty when the
#   cell declares no mock.
recipe_runtime_mock_port() {
  local cell="$1"
  jq -r '.mock.port // empty' <<<"$cell" 2>/dev/null
  return 0
}

# recipe_runtime_mock_pattern <cell-json> → the mock's declared request-log
#   pattern, or empty. See recipe_runtime_mock_check for why it is required.
recipe_runtime_mock_pattern() {
  local cell="$1"
  jq -r '.mock.request_log_pattern // empty' <<<"$cell" 2>/dev/null
  return 0
}

# recipe_runtime_mock_addr <cell-json> → "127.0.0.1:<port>", or empty.
#   The host is FIXED at loopback and is deliberately not a recipe field: a
#   mock reachable off-box is a mock another machine can drive, and a recording
#   rig has no business opening one.
recipe_runtime_mock_addr() {
  local cell="$1" port
  port="$(recipe_runtime_mock_port "$cell")"
  [[ -n "$port" ]] && echo "127.0.0.1:$port"
  return 0
}

# recipe_runtime_mock_check <cell-json>
#   → 0 when the cell declares no mock, or declares a well-formed one.
#     1 + a reason on stderr otherwise.
recipe_runtime_mock_check() {
  local cell="$1" pkg port
  [[ "$(jq -r 'if .mock == null then "n" else "y" end' <<<"$cell" 2>/dev/null)" == "y" ]] || return 0

  pkg="$(jq -r '.mock.package // empty' <<<"$cell")"
  if [[ -z "$pkg" ]]; then
    echo "recipe mock: .mock.package is required (a Go package path, e.g. ./tools/onboarding-factory/recording/mock-anthropic-error)" >&2
    return 1
  fi
  # A package path is joined onto the repo root and handed to `go build`.
  # Confine it to the recording tree so a recipe cannot build and then RUN an
  # arbitrary package out of the repo — the mock is executed, not merely
  # compiled.
  case "$pkg" in
    *..*)
      echo "recipe mock: .mock.package must not contain '..', got \"$pkg\"" >&2
      return 1
      ;;
    ./tools/onboarding-factory/recording/mock-*) ;;
    *)
      echo "recipe mock: .mock.package must name a ./tools/onboarding-factory/recording/mock-* package, got \"$pkg\"" >&2
      return 1
      ;;
  esac

  # The port must be a JSON NUMBER, and the type check is the load-bearing half.
  # A quoted "0900" passes a bare `^[0-9]+$` and then bash reads it as OCTAL in
  # arithmetic — `(( 0900 < 1024 ))` does not evaluate false, it ERRORS, which
  # under this file's `[[ ! … ]] || (( … ))` shape made the whole guard false
  # and ACCEPTED the port. Both halves are fixed: the type check rejects the
  # string outright, and `10#` pins the base for anything that reaches the
  # arithmetic.
  if [[ "$(jq -r 'if (.mock.port | type) == "number" then "y" else "n" end' <<<"$cell")" != "y" ]]; then
    echo "recipe mock: .mock.port must be a JSON number, got $(jq -c '.mock.port // null' <<<"$cell")" >&2
    return 1
  fi
  port="$(jq -r '.mock.port' <<<"$cell")"
  if [[ ! "$port" =~ ^[0-9]+$ ]] || (( 10#$port < 1024 || 10#$port > 65535 )); then
    echo "recipe mock: .mock.port must be an unprivileged port 1024..65535, got \"$port\"" >&2
    return 1
  fi

  # request_log_pattern is REQUIRED, and it is the fix for a guard that
  # inverted its own verdict. run-cell.sh's assert_mock_was_used proves the
  # agent actually reached the mock by counting request lines in the mock's
  # log — but it used to grep a pattern hardcoded to ONE mock's log format
  # (`POST /v1/`). Every other mock in this tree logs something else:
  # mock-gemini-5xx writes `router %s` and `main-turn (abort) %s`. A cell using
  # that mock would have had every one of its requests served and still been
  # told "the mock served ZERO requests — this recording was made against the
  # REAL provider", which is the exact opposite of the truth, produced by the
  # check meant to prevent it.
  #
  # So the pattern belongs to the mock that emits it, declared per cell, and
  # missing is a REFUSAL rather than a default: a default would silently be
  # wrong for every mock but one, which is how this started.
  if [[ -z "$(jq -r '.mock.request_log_pattern // empty' <<<"$cell")" ]]; then
    echo "recipe mock: .mock.request_log_pattern is required — a grep -E pattern matching ONE line per request the mock serves." >&2
    echo "             run-cell.sh counts it to prove the agent reached the mock; there is no safe default, because each mock logs differently." >&2
    return 1
  fi

  if [[ "$(jq -r 'if (.mock.args // []) | type == "array" then "y" else "n" end' <<<"$cell")" != "y" ]]; then
    echo "recipe mock: .mock.args must be an array of strings when present" >&2
    return 1
  fi
  if [[ "$(jq -r '[(.mock.args // [])[] | type] | all(. == "string") | if . then "y" else "n" end' <<<"$cell")" != "y" ]]; then
    echo "recipe mock: .mock.args must be an array of STRINGS" >&2
    return 1
  fi
  return 0
}

# recipe_runtime_env_lines <cell-json> [mock-addr]
#   → one `KEY=VALUE` line per `.env` entry, with `{{MOCK_ADDR}}` and
#     `{{MOCK_PORT}}` substituted. Emits nothing and returns 0 when the cell
#     declares no env.
#     Returns 1 + a reason on stderr for anything it cannot render exactly.
#
#   The output format is what `tmux new-session -e KEY=VALUE` wants, which is
#   also why a value containing a newline is refused rather than escaped: a
#   two-line value would silently become one env var plus one garbage line, and
#   the garbage line is the half nobody would notice.
recipe_runtime_env_lines() {
  local cell="$1" mock_addr="${2:-}" keys k v port
  [[ "$(jq -r 'if .env == null then "n" else "y" end' <<<"$cell" 2>/dev/null)" == "y" ]] || return 0

  if [[ "$(jq -r 'if (.env | type) == "object" then "y" else "n" end' <<<"$cell")" != "y" ]]; then
    echo "recipe env: .env must be an object of NAME → value" >&2
    return 1
  fi
  if [[ "$(jq -r '[.env[] | type] | all(. == "string") | if . then "y" else "n" end' <<<"$cell")" != "y" ]]; then
    echo "recipe env: every .env value must be a string" >&2
    return 1
  fi

  port="${mock_addr##*:}"
  keys="$(jq -r '.env | keys_unsorted[]' <<<"$cell")"
  while IFS= read -r k; do
    [[ -n "$k" ]] || continue
    if [[ ! "$k" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]]; then
      echo "recipe env: \"$k\" is not a valid environment variable name" >&2
      return 1
    fi
    v="$(jq -r --arg k "$k" '.env[$k]' <<<"$cell")"
    if [[ "$v" == *$'\n'* ]]; then
      echo "recipe env: $k's value contains a newline; the driver passes env as one KEY=VALUE line per variable" >&2
      return 1
    fi
    if [[ "$v" == *'{{MOCK_ADDR}}'* || "$v" == *'{{MOCK_PORT}}'* ]]; then
      if [[ -z "$mock_addr" ]]; then
        echo "recipe env: $k references a mock placeholder but the cell declares no .mock block" >&2
        return 1
      fi
      v="${v//\{\{MOCK_ADDR\}\}/$mock_addr}"
      v="${v//\{\{MOCK_PORT\}\}/$port}"
    fi
    # Anything still in {{…}} form is a placeholder this file does not know.
    # Passing it through would export a literal "{{FOO}}" to the agent, which
    # is exactly the shape that reads as "configured" and behaves as "not".
    if [[ "$v" =~ \{\{[A-Za-z0-9_]+\}\} ]]; then
      echo "recipe env: $k still contains an unresolved placeholder after substitution: $v" >&2
      return 1
    fi
    printf '%s=%s\n' "$k" "$v"
  done <<<"$keys"
  return 0
}

# recipe_runtime_driver_gaps <cell-json> <driver-file>
#   → one line per runtime capability the cell NEEDS and the driver does not
#     declare. Empty output = no gap. Always returns 0; the caller decides what
#     a non-empty list means (run-cell.sh refuses).
#
#   This is the refusal that matters most. A driver that silently ignores `env`
#   launches the agent against its real provider with real credentials, and the
#   recording comes back green.
recipe_runtime_driver_gaps() {
  local cell="$1" driver="$2"
  if [[ "$(recipe_runtime_bare "$cell")" == "true" ]] && ! driver_runtime_supports "$driver" bare; then
    echo "bare"
  fi
  if [[ "$(jq -r 'if .env == null then "n" else "y" end' <<<"$cell" 2>/dev/null)" == "y" ]] \
     && ! driver_runtime_supports "$driver" env; then
    echo "env"
  fi
  return 0
}

# recipe_runtime_wait_listening <host> <port> <deadline-seconds>
#   → 0 once something accepts a TCP connection on host:port, 1 at the
#     deadline. Polls; never sleeps a fixed duration and calls it ready.
#     Prints the elapsed time on failure, per AGENTS.md ("a fixture that waits
#     by sleeping hasn't observed what it waits for").
recipe_runtime_wait_listening() {
  local host="$1" port="$2" deadline_s="${3:-15}" waited=0
  while (( waited < deadline_s * 10 )); do
    # -z is a non-consuming probe: it opens and closes without reading, so it
    # cannot eat the mock's first request.
    if nc -z "$host" "$port" 2>/dev/null; then
      return 0
    fi
    sleep 0.1
    waited=$((waited + 1))
  done
  echo "recipe mock: nothing listening on $host:$port after ${deadline_s}s" >&2
  return 1
}

# recipe_runtime_unsupported <coverage-id> <adapter>
#   → one line per runtime-block field the cell declares. Empty = the cell
#     declares none. Always returns 0; the caller decides what a non-empty list
#     means.
#
#   For a rig that does NOT implement the runtime block (run-cell-multi.sh),
#   this is the whole check: any declared field is a refusal, because the rig
#   would silently drive the real provider. It reads the recipe itself rather
#   than taking a pre-projected CELL_JSON, so a caller that never learned about
#   these fields still gets a correct answer.
recipe_runtime_unsupported() {
  local coverage_id="$1" adapter="$2" recipe
  recipe="$(shard_recipe "$coverage_id" "$adapter" 2>/dev/null)"
  [[ -n "$recipe" ]] || return 0
  jq -r '
    [ (if .bare_mode == true then "bare_mode" else empty end),
      (if .env  != null then "env"  else empty end),
      (if .mock != null then "mock" else empty end) ] | .[]
  ' <<<"$recipe" 2>/dev/null
  return 0
}

# ---------------------------------------------------------------------------
# Post-drive assertions
# ---------------------------------------------------------------------------
#
# These two lived inside run-cell.sh until #1803's review pointed out the
# consequence: run-cell.sh is a pipeline script, not a sourceable library, so
# the two checks that decide whether a recording is TRUSTWORTHY were the only
# part of this feature with no committed mutation fixture. They were exercised
# by three live recordings and nothing else. Moved here, they are unit-tested
# and mutated like everything else in this file.

# recipe_runtime_assert_env_receipt <staging> <driver-label>
#   → 0 when the driver applied exactly the runtime set the recipe asked for
#     (or the recipe asked for none). 1 + a reason otherwise.
#
# THE WHOLE POINT of the runtime block is that the agent talks to the mock and
# not to the real provider, and every way that can fail is silent: a driver
# that ignores $STAGING/driver-env produces a recording that looks perfect and
# was made against production credentials. DRIVE_SUPPORTS is a claim the driver
# makes about itself; this is the check that it did the thing.
#
# The receipt carries NAMES only, never values: a recipe may legitimately put a
# credential in .env, and staging is copied into the promoted fixture.
recipe_runtime_assert_env_receipt() {
  local staging="$1" driver_label="$2" want got
  want="$( { [[ -s "$staging/driver-env" ]] && cut -d= -f1 "$staging/driver-env"
             [[ -f "$staging/driver-bare" ]] && echo "__bare__"; } | sort -u)"
  [[ -n "$want" ]] || return 0
  if [[ ! -f "$staging/driver-env.applied" ]]; then
    echo "runtime_gap: the recipe asked for a runtime env/bare_mode but $driver_label wrote no receipt" >&2
    echo "  → the agent was almost certainly driven against the REAL provider; discarding this run." >&2
    return 1
  fi
  got="$(sort -u "$staging/driver-env.applied")"
  if [[ "$want" != "$got" ]]; then
    echo "runtime_gap: $driver_label applied a different runtime set than the recipe asked for" >&2
    echo "  want: $(tr '\n' ' ' <<<"$want")" >&2
    echo "  got:  $(tr '\n' ' ' <<<"$got")" >&2
    return 1
  fi
  echo "runtime: $driver_label applied [$(tr '\n' ' ' <<<"$got")]"
  return 0
}

# recipe_runtime_assert_mock_used <staging> <cell-json> <mock-addr>
#   → 0 when the mock's own request log proves the agent reached it.
#     1 + a reason otherwise. No-op when the cell declares no mock.
#
# The only check on this path that observes the SUBJECT rather than a claim
# about it. Everything else is a statement someone made — the recipe says where
# the agent should point, DRIVE_SUPPORTS says the driver can do that, the
# receipt says it did. This says whether the AGENT actually arrived.
#
# THE PATTERN COMES FROM THE RECIPE, never from here. It was once a hardcoded
# `POST /v1/`, which is one mock's log format: mock-gemini-5xx logs `router %s`
# and `main-turn (abort) %s`, so a cell using it would have had every request
# served and still been told the recording was made against the real provider —
# the exact opposite of the truth, from the guard meant to prevent it.
#
# Three outcomes, three messages. "I could not read the log" must not look like
# "the mock served nothing", which must not look like success.
recipe_runtime_assert_mock_used() {
  local staging="$1" cell="$2" mock_addr="$3" pattern hits
  [[ -n "$mock_addr" ]] || return 0
  pattern="$(recipe_runtime_mock_pattern "$cell")"
  if [[ -z "$pattern" ]]; then
    # Unreachable through run-cell.sh (recipe_runtime_mock_check refuses first),
    # stated rather than assumed: a silent `grep -E ""` matches every line and
    # would pass on the mock's startup banner alone.
    echo "runtime_gap: the cell declares a mock but no .mock.request_log_pattern — cannot tell whether the agent reached it" >&2
    return 1
  fi
  if [[ ! -r "$staging/mock.log" ]]; then
    echo "runtime_gap: the cell's mock log at $staging/mock.log is missing or unreadable" >&2
    echo "  → this is NOT 'the mock served nothing'; it is 'this check could not run'. Do not promote." >&2
    return 1
  fi
  hits="$(grep -cE "$pattern" "$staging/mock.log" || true)"
  if [[ "$hits" -eq 0 ]]; then
    echo "runtime_gap: the cell's mock on $mock_addr served ZERO requests matching /$pattern/" >&2
    echo "  → either the agent did not talk to it — so this recording was made against the REAL provider —" >&2
    echo "    or .mock.request_log_pattern does not match what this mock actually logs. Check both." >&2
    sed 's/^/  mock: /' "$staging/mock.log" >&2 2>/dev/null || true
    return 1
  fi
  echo "runtime: the cell's mock served $hits request(s) matching /$pattern/"
  return 0
}
