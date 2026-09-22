#!/usr/bin/env bash
# endpoint-route-mutations_test.sh — the committed mutation fixtures for
# #2002's two new guards, neither of which has a "before the fix" to run red
# (docs/testing-philosophy.md: "anything a change ADDS ... owes a deliberate
# mutation instead"):
#
#   1. redactEndpoint's query-string strip (core/adapters/inbound/agents/
#      processlifecycle/endpoint_route.go): a base URL's query string must
#      never survive redaction. Protected by TestRedactEndpoint's
#      "strips userinfo, query, and fragment; keeps path" case.
#   2. ObserveRoute's unreadable-environment branch (same file): an EnvOf
#      read that failed must report Unreadable, never collapse into Absent.
#      Protected by TestObserveRoute_Unreadable.
#
# Both were run by hand via tools/mutate.sh during #2002 and pasted into the
# PR/report; this file is that same pair of perturbations, committed so they
# re-run rather than living only in a commit message (AGENTS.md: "prefer
# committing that mutation to describing it"). Follows
# tools/lib/cost-unattributed-mutations_test.sh directly — same
# assert_go_test_goes_red shape, same tools/mutate.sh underneath.
#
# tools/mutate.sh owns the mechanics this file must not re-improvise: the
# stale/ambiguous-anchor guards (a mutation that matched zero or more than
# one site fails loudly rather than silently doing nothing or the wrong
# thing), and the byte-for-byte restore that never touches git state
# (worktrees share the parent repo's .git dir, so `git checkout --` /
# `git restore` / `git reset --hard` are banned repo-wide).

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
MUTATE_SH="$REPO_ROOT/tools/mutate.sh"

# A missing tool is a hard failure, not a skip — exiting 0 here would read as
# a PASS to preflight's shell_lib_tests, so the gate would go green having
# asserted nothing.
need() {
  local tool="$1"
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "FAIL: endpoint-route-mutations — $tool not found" >&2
    exit 1
  fi
  return 0
}

# Indents captured output so it reads as a quoted block under its FAIL line.
quote_output() {
  sed 's/^/      | /'
  return 0
}
need go
need git

if [[ ! -x "$MUTATE_SH" ]]; then
  echo "FAIL: endpoint-route-mutations — $MUTATE_SH is missing or not executable" >&2
  exit 1
fi

# mutate.sh refuses (exit 4) against an already-dirty tree, because its
# post-restore emptiness check could prove nothing then.
#
# A DIRTY TREE MUST NOT SILENTLY PASS: this suite's runner (shell-lib-suite.sh)
# judges a script by its EXIT STATUS and has no self-skip protocol, so an
# `exit 0` here would make "the guard was verified" and "the guard could not
# be checked at all" produce byte-identical results at the gate. So it is a
# HARD FAILURE wherever the answer is load-bearing (CI, and any caller that
# sets MUTATION_FIXTURES_STRICT=1), and a loud, non-silent skip on a
# developer's dirty worktree, where failing would only train people to delete
# the fixture.
if [[ -n "$(git -C "$REPO_ROOT" status --porcelain)" ]]; then
  echo "endpoint-route-mutations: CANNOT RUN — the worktree is dirty, and mutate.sh needs a" >&2
  echo "  clean tree for its post-restore check to mean anything. Commit or clean up, then:" >&2
  echo "    bash tools/lib/endpoint-route-mutations_test.sh" >&2
  if [[ -n "${CI:-}" || -n "${MUTATION_FIXTURES_STRICT:-}" ]]; then
    echo "  (failing rather than skipping: CI/strict mode, where a skip is indistinguishable" >&2
    echo "   from a pass and this gate is the only thing re-running these mutations)" >&2
    exit 1
  fi
  echo "  (skipped locally; set MUTATION_FIXTURES_STRICT=1 to make this a failure)" >&2
  exit 0
fi

fails=0

# assert_go_test_goes_red <label> <file> <anchor> <replacement> <pkg> <run-regex> <want-in-output>
#
# Applies one mutation and requires the named Go test to FAIL under it, AND
# to fail for the right reason. Both halves matter: a mutation that leaves
# the test green means the guard does not reach what it claims to protect,
# and a mutation that goes red because the package no longer COMPILES would
# otherwise read as success.
assert_go_test_goes_red() {
  local label="$1" file="$2" anchor="$3" replacement="$4" pkg="$5" run="$6" want="$7"
  local out rc

  out="$(cd "$REPO_ROOT" && "$MUTATE_SH" "$file" "$anchor" "$replacement" \
    bash -c "go test $pkg -run '$run' -count=1 2>&1; echo GO_TEST_RC=\$?" 2>&1)"
  rc=$?

  if [[ $rc -ne 0 ]]; then
    echo "FAIL: $label — mutate.sh refused (exit $rc). A STALE anchor means the surrounding text"
    echo "      moved and this fixture needs its anchor updated; it does NOT mean the guard is fine."
    echo "$out" | quote_output
    fails=$((fails + 1))
    return
  fi
  if grep -q 'GO_TEST_RC=0' <<<"$out"; then
    echo "FAIL: $label — the test stayed GREEN under the mutation, so the guard does not reach"
    echo "      what it claims to protect."
    echo "$out" | quote_output
    fails=$((fails + 1))
    return
  fi
  if grep -qE '^# |build failed|cannot use|undefined:' <<<"$out"; then
    echo "FAIL: $label — the mutation broke the BUILD rather than the guard. A fixture that"
    echo "      cannot compile proves nothing about the behavior it is meant to exercise."
    echo "$out" | quote_output
    fails=$((fails + 1))
    return
  fi
  if ! grep -qF "$want" <<<"$out"; then
    echo "FAIL: $label — the test failed, but not with the expected message."
    echo "      wanted to find: $want"
    echo "$out" | quote_output
    fails=$((fails + 1))
    return
  fi
  echo "ok  $label"
}

# ── redactEndpoint's query strip ────────────────────────────────────────────
# Disabling the RawQuery clear must leave the query string in the redacted
# endpoint — exactly the retention #2002 §1.3 forbids.
assert_go_test_goes_red \
  "disabling the query-string strip reddens the redaction test" \
  "core/adapters/inbound/agents/processlifecycle/endpoint_route.go" \
  'u.RawQuery = ""' \
  '_ = u.RawQuery // mutated: query strip disabled (#2002 fixture)' \
  "./core/adapters/inbound/agents/processlifecycle/..." \
  "TestRedactEndpoint" \
  'want "https://api.example.com/v1/base"'

# ── ObserveRoute's unreadable branch ────────────────────────────────────────
# An EnvOf read that failed must report Unreadable — collapsing it into
# Absent is exactly the confusion #2002 §1's fixture table forbids (an
# operator who denied every reader must not look identical to a session with
# no override configured).
assert_go_test_goes_red \
  "collapsing unreadable into absent reddens the unreadable-branch test" \
  "core/adapters/inbound/agents/processlifecycle/endpoint_route.go" \
  'return &session.RouteObservation{Status: session.RouteUnreadable}' \
  'return &session.RouteObservation{Status: session.RouteAbsent} // mutated: unreadable collapsed into absent (#2002 fixture)' \
  "./core/adapters/inbound/agents/processlifecycle/..." \
  "TestObserveRoute_Unreadable" \
  'want Status=unreadable'

if [[ $fails -gt 0 ]]; then
  echo "endpoint-route-mutations: $fails mutation(s) did not behave as required" >&2
  exit 1
fi
echo "endpoint-route-mutations: ok"
