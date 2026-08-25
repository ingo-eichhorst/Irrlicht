#!/usr/bin/env bash
# recipe-runtime_test.sh — unit tests for lib/recipe-runtime.sh. Plain bash +
# jq (no framework). Run directly or via scripts/smoke-test.sh. Exits non-zero
# on any failed assertion.
#
# WHAT THIS IS EVIDENCE FOR. recipe-runtime.sh is entirely NEW checking, so it
# has no "before the fix" to run red (AGENTS.md). The evidence is therefore a
# mutation battery: every refusal below is paired with the input it exists to
# reject, and the fixtures ARE the mutations — a committed corpus row per
# rejection rather than a paragraph in a PR body that nothing re-runs.
#
# WHICH ASSERTIONS ARE EVIDENCE, AND WHICH ARE LOCKS. Not all of them are the
# same thing, and presenting a green that was never red as proof is the failure
# AGENTS.md names. recipe-runtime-mutation_test.sh reddens FIFTEEN clauses, and
# those assertions are evidence:
#
#   package confinement . port type . port range . request_log_pattern required
#   bare_mode true-vs-"true" . unresolved placeholder . newline in an env value
#   env name shape . driver gaps . readiness probe . env receipt required
#   env receipt compared . mock log readable . mock zero hits . mock pattern
#
# The rest are LOCKS — they pass by construction and no mutation was written
# for them, because each shares a clause with one above or guards a shape the
# schema already forbids:
#
#   "mock without package", "mock package with ..", "mock without port",
#   "mock port above range", "mock non-numeric port", "mock args not an array",
#   "mock args not strings", "env is not an object", "env value is not a
#   string", "env name starts with a digit", "mock with an EMPTY
#   request_log_pattern"
#
# They are worth keeping — they pin the schema against a future edit — but
# their green is not red-first proof of anything, and this comment is here so
# nobody reports it as such.

set -uo pipefail   # NOT -e: assertions capture non-zero return codes

DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=recipe-runtime.sh
source "$DIR/recipe-runtime.sh"

command -v jq >/dev/null || { echo "recipe-runtime_test: jq is required" >&2; exit 2; }

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

fails=0
assert_eq() {
  local what="$1" want="$2" got="$3"
  if [[ "$want" == "$got" ]]; then
    echo "  ok   $what"
  else
    echo "  FAIL $what: want [$want] got [$got]" >&2
    fails=$((fails + 1))
  fi
  return 0
}
# assert_refuses <what> <function-invocation...> — the function must return
# non-zero AND print a reason. A silent refusal is as bad as a silent accept:
# run-cell.sh surfaces the reason, so a rejection with no message strands the
# operator with an exit code and no cause.
assert_refuses() {
  local what="$1"; shift
  local err rc
  err="$("$@" 2>&1 >/dev/null)"; rc=$?
  if [[ "$rc" -eq 0 ]]; then
    echo "  FAIL $what: accepted (rc=0) — the refusal does not fire" >&2
    fails=$((fails + 1))
  elif [[ -z "$err" ]]; then
    echo "  FAIL $what: refused (rc=$rc) but printed no reason" >&2
    fails=$((fails + 1))
  else
    echo "  ok   $what — refused: ${err%%$'\n'*}"
  fi
  return 0
}

# assert_refuses_with <what> <expected-reason-substring> <invocation...>
# Like assert_refuses, but pins the REASON. Some guards have two clauses that
# both reject the same input, so disabling either still refuses — the clause
# earns its place through the MESSAGE it produces, and only asserting the
# message can show it red. (#1800 met the same masking and deleted the
# redundant guard; here the distinct message is the point, so it is pinned
# instead.)
assert_refuses_with() {
  local what="$1" want="$2"; shift 2
  local err rc
  err="$("$@" 2>&1 >/dev/null)"; rc=$?
  if [[ "$rc" -eq 0 ]]; then
    echo "  FAIL $what: accepted (rc=0) — the refusal does not fire" >&2
    fails=$((fails + 1))
  elif [[ "$err" != *"$want"* ]]; then
    echo "  FAIL $what: refused, but the reason does not say \"$want\"" >&2
    echo "       got: ${err%%$'\n'*}" >&2
    fails=$((fails + 1))
  else
    echo "  ok   $what — refused: ${err%%$'\n'*}"
  fi
  return 0
}

echo "== driver capability contract =="
cat > "$TMP/driver-both.sh" <<'SH'
#!/usr/bin/env bash
DRIVE_SUPPORTS="env bare"   # trailing comment must not break the scrape
SH
cat > "$TMP/driver-env-only.sh" <<'SH'
#!/usr/bin/env bash
DRIVE_SUPPORTS='env'
SH
cat > "$TMP/driver-none.sh" <<'SH'
#!/usr/bin/env bash
DRIVE_ELICITS="send sleep"
SH

driver_runtime_supports "$TMP/driver-both.sh" env;  assert_eq "declares env"  "0" "$?"
driver_runtime_supports "$TMP/driver-both.sh" bare; assert_eq "declares bare" "0" "$?"
driver_runtime_supports "$TMP/driver-env-only.sh" bare; assert_eq "env-only driver does not declare bare" "1" "$?"
driver_runtime_supports "$TMP/driver-none.sh" env;  assert_eq "no constant → not supported" "1" "$?"
driver_runtime_supports "$TMP/nope.sh" env;         assert_eq "missing file → not supported" "1" "$?"

echo "== bare_mode =="
assert_eq "bare_mode true"      "true"  "$(recipe_runtime_bare '{"bare_mode":true}')"
assert_eq "bare_mode false"     "false" "$(recipe_runtime_bare '{"bare_mode":false}')"
assert_eq "bare_mode absent"    "false" "$(recipe_runtime_bare '{}')"
# The string "true" is NOT true. A recipe that spells it as a string would
# otherwise silently run WITHOUT --bare, and claude would prefer the operator's
# real keychain credentials over the mock's — the exact silent-real-provider
# failure this library exists to prevent.
assert_eq "bare_mode \"true\" (string) is not true" "false" "$(recipe_runtime_bare '{"bare_mode":"true"}')"

echo "== mock block validation =="
# One fixture builder rather than the same 90-character literal in a dozen
# assertions: a valid mock block now needs four fields, and hand-repeating it
# is how one of them silently stops being exercised.
MOCK_PKG="./tools/onboarding-factory/recording/mock-anthropic-error"
MOCK_PAT="POST /v1/messages #[0-9]+"
# mock_json [key value]... — a well-formed mock, with overrides applied as RAW
# JSON so a caller can pass a number, a string or a wrong type on purpose.
mock_json() {
  local out; out="$(jq -cn --arg p "$MOCK_PKG" --arg r "$MOCK_PAT" \
    '{mock:{package:$p, port:18767, request_log_pattern:$r}}')"
  local key val
  while [[ $# -gt 0 ]]; do
    key="$1"; val="$2"; shift 2
    out="$(jq -c --arg k "$key" --argjson v "$val" '.mock[$k] = $v' <<<"$out")"
  done
  printf '%s' "$out"
  return 0
}

recipe_runtime_mock_check '{}'; assert_eq "no mock block → accepted" "0" "$?"
recipe_runtime_mock_check "$(mock_json)"
assert_eq "well-formed mock → accepted" "0" "$?"
recipe_runtime_mock_check "$(mock_json args '["--status","529"]')"
assert_eq "well-formed mock with args → accepted" "0" "$?"

assert_refuses "mock without package"        recipe_runtime_mock_check '{"mock":{"port":18767,"request_log_pattern":"x"}}'
assert_refuses "mock package outside recording tree" \
  recipe_runtime_mock_check "$(mock_json package '"./core/cmd/irrlichd"')"
assert_refuses "mock package with .." \
  recipe_runtime_mock_check "$(mock_json package '"./tools/onboarding-factory/recording/mock-x/../../../core"')"
assert_refuses "mock without port"           recipe_runtime_mock_check "{\"mock\":{\"package\":\"$MOCK_PKG\",\"request_log_pattern\":\"x\"}}"
assert_refuses "mock privileged port"        recipe_runtime_mock_check "$(mock_json port 80)"
assert_refuses "mock port above range"       recipe_runtime_mock_check "$(mock_json port 70000)"
assert_refuses "mock non-numeric port"       recipe_runtime_mock_check "$(mock_json port '"http"')"
# A port as a JSON STRING is refused by the type check even when it would pass
# the numeric range — the recipe schema says number, and a string is how the
# octal hazard below arrives in the first place.
assert_refuses "mock port as a JSON string"  recipe_runtime_mock_check "$(mock_json port '"8080"')"
# The octal case that started this: bash reads 0900 in arithmetic as an invalid
# octal literal, which ERRORS rather than evaluating false, and under the
# guard's `[[ ! … ]] || (( … ))` shape that made the whole condition false and
# ACCEPTED the port. Defended twice now — the type check rejects the string
# before the arithmetic, and `10#` pins the base. This assertion exercises the
# first; the `10#` half is unreachable while the type check stands and is kept
# as deliberate defence in depth, NOT claimed as covered.
assert_refuses "mock port \"0900\" (octal-looking string)" recipe_runtime_mock_check "$(mock_json port '"0900"')"
assert_refuses "mock args not an array"      recipe_runtime_mock_check "$(mock_json args '"--status 529"')"
assert_refuses "mock args not strings"       recipe_runtime_mock_check "$(mock_json args '[529]')"
# request_log_pattern is what run-cell.sh greps to prove the agent reached the
# mock. Absent, that check cannot tell "served nothing" from "wrong pattern",
# so the recipe is refused before a recording is attempted.
assert_refuses "mock without request_log_pattern" \
  recipe_runtime_mock_check "{\"mock\":{\"package\":\"$MOCK_PKG\",\"port\":18767}}"
assert_refuses "mock with an EMPTY request_log_pattern" \
  recipe_runtime_mock_check "$(mock_json request_log_pattern '""')"

assert_eq "mock addr" "127.0.0.1:18767" "$(recipe_runtime_mock_addr "$(mock_json)")"
assert_eq "mock addr absent" "" "$(recipe_runtime_mock_addr '{}')"
assert_eq "mock pattern" "$MOCK_PAT" "$(recipe_runtime_mock_pattern "$(mock_json)")"
assert_eq "mock pattern absent" "" "$(recipe_runtime_mock_pattern '{}')"

echo "== env rendering =="
assert_eq "no env → no lines" "" "$(recipe_runtime_env_lines '{}')"
assert_eq "plain env" "ANTHROPIC_API_KEY=sk-mock" \
  "$(recipe_runtime_env_lines '{"env":{"ANTHROPIC_API_KEY":"sk-mock"}}')"
assert_eq "MOCK_ADDR substitution" "ANTHROPIC_BASE_URL=http://127.0.0.1:18767" \
  "$(recipe_runtime_env_lines '{"env":{"ANTHROPIC_BASE_URL":"http://{{MOCK_ADDR}}"}}' '127.0.0.1:18767')"
assert_eq "MOCK_PORT substitution" "PORT=18767" \
  "$(recipe_runtime_env_lines '{"env":{"PORT":"{{MOCK_PORT}}"}}' '127.0.0.1:18767')"
assert_eq "an empty value is legal (claude reads CLAUDE_CODE_LOOP_PERSISTENT= as unset)" "X=" \
  "$(recipe_runtime_env_lines '{"env":{"X":""}}')"
# Key order is the recipe's, not jq's sort: an operator reading driver-env
# should see what they wrote.
assert_eq "keys keep recipe order" "B=2
A=1" "$(recipe_runtime_env_lines '{"env":{"B":"2","A":"1"}}')"

assert_refuses "placeholder with no mock" \
  recipe_runtime_env_lines '{"env":{"ANTHROPIC_BASE_URL":"http://{{MOCK_ADDR}}"}}'
assert_refuses "unknown placeholder survives substitution" \
  recipe_runtime_env_lines '{"env":{"X":"{{NOT_A_THING}}"}}' '127.0.0.1:18767'
assert_refuses "env is not an object" recipe_runtime_env_lines '{"env":["A=1"]}'
assert_refuses "env value is not a string" recipe_runtime_env_lines '{"env":{"A":1}}'
assert_refuses "env name is not a shell identifier" recipe_runtime_env_lines '{"env":{"A-B":"1"}}'
assert_refuses "env name starts with a digit" recipe_runtime_env_lines '{"env":{"1A":"1"}}'
assert_refuses "env value contains a newline" \
  recipe_runtime_env_lines "$(jq -cn '{env:{A:"one\ntwo"}}')"

echo "== driver gaps (the loud refusal) =="
assert_eq "no runtime block → no gaps" "" \
  "$(recipe_runtime_driver_gaps '{}' "$TMP/driver-none.sh")"
assert_eq "env on a driver that supports it → no gap" "" \
  "$(recipe_runtime_driver_gaps '{"env":{"A":"1"}}' "$TMP/driver-both.sh")"
assert_eq "env on a driver that does NOT → gap" "env" \
  "$(recipe_runtime_driver_gaps '{"env":{"A":"1"}}' "$TMP/driver-none.sh")"
assert_eq "bare on a driver that does NOT → gap" "bare" \
  "$(recipe_runtime_driver_gaps '{"bare_mode":true}' "$TMP/driver-env-only.sh")"
assert_eq "both missing → both named" "bare
env" "$(recipe_runtime_driver_gaps '{"bare_mode":true,"env":{"A":"1"}}' "$TMP/driver-none.sh")"

echo "== readiness wait observes the SUBJECT, not a sleep =="
# A port nothing listens on must fail at the deadline, not pass by timing out
# quietly. 1s deadline keeps the suite fast; the assertion is the return code.
recipe_runtime_wait_listening 127.0.0.1 1 1 2>/dev/null
assert_eq "nothing listening → refuses" "1" "$?"
# And a port something DOES listen on must be observed. Bound here with nc so
# the test proves the probe can actually succeed — a wait that always returned
# 1 would pass the assertion above and be useless.
# The port is overridable and, whichever it is, the test PROVES the listener is
# ours before trusting the probe. A hardcoded port shared by every copy of this
# suite (the mutation harness forks eight) made the check observe someone
# else's socket: with a stray listener already bound, our own `nc -l` loses the
# bind, exits immediately, and the probe still returns 0 — reporting "observed"
# on evidence that has nothing to do with this run.
NC_PORT="${IRR_TEST_NC_PORT:-0}"
if [[ "$NC_PORT" == "0" ]]; then
  # A per-process port keeps concurrent copies off each other's socket without
  # needing the shell to report an ephemeral bind (`nc -l 0` does not tell us
  # which port it got).
  NC_PORT=$(( 19000 + ($$ % 900) ))
fi
if nc -z 127.0.0.1 "$NC_PORT" 2>/dev/null; then
  echo "  FAIL listening → observed: port $NC_PORT was ALREADY in use, so this check cannot run" >&2
  echo "       (set IRR_TEST_NC_PORT to a free port)" >&2
  fails=$((fails + 1))
else
  # -k (keep inbound sockets open for multiple connects). Without it `nc -l`
  # is ONE-SHOT: the bind-verification probe below connects, consumes the
  # listener, and the assertion then measures a port nothing is on — the
  # check would fail for a reason that has nothing to do with the subject.
  nc -k -l 127.0.0.1 "$NC_PORT" >/dev/null 2>&1 &
  NC_PID=$!
  # Our listener has to actually come up, or the assertion below proves nothing.
  bound=0
  for _ in 1 2 3 4 5 6 7 8 9 10; do
    if nc -z 127.0.0.1 "$NC_PORT" 2>/dev/null; then bound=1; break; fi
    sleep 0.1
  done
  if [[ "$bound" -ne 1 ]]; then
    echo "  FAIL listening → observed: our own nc never bound $NC_PORT — check could not run" >&2
    fails=$((fails + 1))
  else
    recipe_runtime_wait_listening 127.0.0.1 "$NC_PORT" 5
    assert_eq "listening → observed" "0" "$?"
  fi
  kill "$NC_PID" 2>/dev/null
  wait "$NC_PID" 2>/dev/null
fi

echo "== post-drive assertions: the env receipt =="
# These two decide whether a recording is TRUSTWORTHY, and until #1803's review
# they lived inside run-cell.sh where nothing could reach them.
mkstage() { local d; d="$(mktemp -d "$TMP/stage.XXXXXX")"; printf '%s' "$d"; return 0; }

st="$(mkstage)"
recipe_runtime_assert_env_receipt "$st" drv >/dev/null
assert_eq "recipe asked for nothing → nothing to assert" "0" "$?"

st="$(mkstage)"; printf 'A=1
B=2
' > "$st/driver-env"
assert_refuses_with "env requested, NO receipt written" "wrote no receipt" \
  recipe_runtime_assert_env_receipt "$st" drv

st="$(mkstage)"; printf 'A=1
B=2
' > "$st/driver-env"; printf 'A
B
' > "$st/driver-env.applied"
recipe_runtime_assert_env_receipt "$st" drv >/dev/null
assert_eq "receipt matches → accepted" "0" "$?"

st="$(mkstage)"; printf 'A=1
B=2
' > "$st/driver-env"; printf 'A
' > "$st/driver-env.applied"
assert_refuses "receipt is MISSING a variable the recipe asked for" \
  recipe_runtime_assert_env_receipt "$st" drv

st="$(mkstage)"; printf 'A=1
' > "$st/driver-env"; printf 'A
Z
' > "$st/driver-env.applied"
assert_refuses "receipt claims a variable the recipe did NOT ask for" \
  recipe_runtime_assert_env_receipt "$st" drv

# bare_mode is receipted under its own token, so a driver that honors env and
# silently drops --bare is caught. Dropping --bare is the worst single failure
# on this path: claude then prefers the operator's real keychain credentials.
st="$(mkstage)"; : > "$st/driver-bare"
assert_refuses_with "bare requested, NO receipt" "wrote no receipt" \
  recipe_runtime_assert_env_receipt "$st" drv
st="$(mkstage)"; : > "$st/driver-bare"; printf '__bare__
' > "$st/driver-env.applied"
recipe_runtime_assert_env_receipt "$st" drv >/dev/null
assert_eq "bare receipted → accepted" "0" "$?"
st="$(mkstage)"; : > "$st/driver-bare"; printf 'A=1
' > "$st/driver-env"; printf 'A
' > "$st/driver-env.applied"
assert_refuses "env receipted but --bare silently dropped" \
  recipe_runtime_assert_env_receipt "$st" drv

echo "== post-drive assertions: the mock was actually reached =="
MOCKED="$(mock_json)"
MOCK_ADDR_1="127.0.0.1:18767"
MOCK_ADDR_2="127.0.0.1:18770"
st="$(mkstage)"
recipe_runtime_assert_mock_used "$st" '{}' "" >/dev/null
assert_eq "no mock declared → nothing to assert" "0" "$?"

st="$(mkstage)"
assert_refuses_with "mock declared but its log is unreadable" "could not run" \
  recipe_runtime_assert_mock_used "$st" "$MOCKED" "$MOCK_ADDR_1"

# The startup banner alone must NOT count: the mock writes it before serving
# anything, so "the log is non-empty" proves only that the mock ran.
st="$(mkstage)"; printf 'mock-anthropic-error listening on 127.0.0.1:18767
' > "$st/mock.log"
assert_refuses "banner only → the mock served nothing" \
  recipe_runtime_assert_mock_used "$st" "$MOCKED" "$MOCK_ADDR_1"

st="$(mkstage)"
printf 'mock listening
POST /v1/messages #1 model=x — failing 529
POST /v1/messages #2 model=x — succeeding
' > "$st/mock.log"
recipe_runtime_assert_mock_used "$st" "$MOCKED" "$MOCK_ADDR_1" >/dev/null
assert_eq "two served requests → accepted" "0" "$?"

# The finding this whole field exists for: a mock whose log format is not
# claudecode's must still be counted correctly. mock-gemini-5xx logs `router %s`
# and nothing resembling `POST /v1/`.
GEMINI_CELL="$(jq -cn '{mock:{package:"./tools/onboarding-factory/recording/mock-gemini-5xx",
                             port:18770, request_log_pattern:"^router "}}')"
st="$(mkstage)"; printf 'listening on 127.0.0.1:18770
router main-turn
router main-turn
' > "$st/mock.log"
recipe_runtime_assert_mock_used "$st" "$GEMINI_CELL" "$MOCK_ADDR_2" >/dev/null
assert_eq "a NON-claudecode mock log is counted by its own pattern" "0" "$?"
# ...and the same log under claudecode's pattern must NOT be reported as
# "served zero" success — it is a refusal, which is what makes the hardcoded
# pattern a bug rather than a cosmetic difference.
CLAUDE_PAT_ON_GEMINI="$(jq -cn '{mock:{package:"./tools/onboarding-factory/recording/mock-gemini-5xx",
                                       port:18770, request_log_pattern:"POST /v1/"}}')"
assert_refuses "the WRONG pattern refuses rather than passing" \
  recipe_runtime_assert_mock_used "$st" "$CLAUDE_PAT_ON_GEMINI" "$MOCK_ADDR_2"

st="$(mkstage)"; printf 'router main-turn
' > "$st/mock.log"
assert_refuses "mock declared with no request_log_pattern" \
  recipe_runtime_assert_mock_used "$st" '{"mock":{"port":18770}}' "$MOCK_ADDR_2"

echo ""
if [[ "$fails" -eq 0 ]]; then
  echo "recipe-runtime_test: ALL PASS"
else
  echo "recipe-runtime_test: $fails FAILED" >&2
  exit 1
fi
