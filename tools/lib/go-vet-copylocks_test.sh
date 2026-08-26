#!/usr/bin/env bash
# go-vet-copylocks_test.sh — the committed mutation fixture for #1823's
# `go vet` addition to tools/preflight.sh's `go` group (the "go vet (core)"
# and "go vet (onboarding-factory)" gates).
#
# WHY THIS FILE EXISTS. Those gates are a check the #1823 fix ADDS: it has
# no "before the fix" to run red, so per AGENTS.md and
# docs/testing-philosophy.md it earns its place only by being seen to fail
# when the thing it protects is broken.
#
# The specific claim being proven is narrower than "go vet catches bugs" —
# it is that `go vet` catches something `go test` (even under `-race`, which
# is what tools/preflight.sh's own core module tests run) does NOT, so
# adding `go vet` as its own gate is not redundant with the test suite
# already there. `go test` runs a small "high confidence" subset of vet
# automatically before compiling test binaries (atomic, bool, buildtags,
# directive, errorsas, ifaceassert, nilfunc, printf, stringintconv) and that
# subset excludes copylocks — passing or copying a value that contains a
# sync.Mutex (or any sync.Locker). A copylocks violation is dead-simple to
# introduce (a function declaring a by-value parameter of a lock-bearing
# struct type) and needs no runtime exercise to be wrong: `go vet` flags it
# at the declaration site, `go test` compiles and runs it clean regardless.
#
# The mutation adds exactly that: a never-called function taking
# ConcurrencyTracker (which embeds resolveMu sync.Mutex) by value, into
# core/adapters/outbound/filesystem/concurrency_tracker.go. Both checks run
# under the SAME mutation in one tools/mutate.sh call, so the contrast is
# read from a single, simultaneous snapshot rather than two separate runs
# that could each have drifted:
#   - `go vet ./core/adapters/outbound/filesystem/...`  MUST go red
#   - `go test ./core/adapters/outbound/filesystem/... -count=1`  MUST stay green
#
# Scoped to the one package rather than the whole core module (unlike the
# "go vet (core)" gate itself, which covers ./core/...) purely for fixture
# runtime — this runs on every `tools/preflight.sh --only tools`, and the package-level
# `go test` here is ~1s against core module tests' multi-minute full run.
# The scope of the PROOF (one package) is independent of the scope of the
# GUARD it backs (the whole module); the guard covers everything, the fixture
# only needs one reachable example.
#
# tools/mutate.sh owns the mechanics this file must not re-improvise: the
# stale-anchor guard, the no-op replacement refusal, and the byte-for-byte
# restore that never touches git state (worktrees share the parent repo's
# .git dir, so `git checkout --` / `git restore` / `git reset --hard` are
# banned repo-wide).

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
MUTATE_SH="$REPO_ROOT/tools/mutate.sh"
TARGET_FILE="core/adapters/outbound/filesystem/concurrency_tracker.go"
PKG="./core/adapters/outbound/filesystem/..."

# A missing tool is a hard failure, not a skip — exiting 0 here would read as
# a PASS to preflight's shell_lib_tests, so the gate would go green having
# asserted nothing.
need() { command -v "$1" >/dev/null 2>&1 || { echo "FAIL: go-vet-copylocks — $1 not found" >&2; exit 1; }; }
need go
need git

if [[ ! -x "$MUTATE_SH" ]]; then
  echo "FAIL: go-vet-copylocks — $MUTATE_SH is missing or not executable" >&2
  exit 1
fi
if [[ ! -f "$REPO_ROOT/$TARGET_FILE" ]]; then
  echo "FAIL: go-vet-copylocks — $TARGET_FILE not found" >&2
  exit 1
fi

# mutate.sh refuses (exit 4) against an already-dirty tree, because its
# post-restore emptiness check could prove nothing then.
#
# A DIRTY TREE MUST NOT SILENTLY PASS. This suite's runner (shell-lib-suite.sh)
# judges a script by its EXIT STATUS and has no self-skip protocol, so an
# `exit 0` here would make "the guard was verified" and "the guard could not
# be checked at all" produce byte-identical results at the gate — exactly the
# shape AGENTS.md forbids, and the same shape as the tripwire blind spot this
# fixture exists to prevent regressing.
#
# So it is a HARD FAILURE wherever the answer is load-bearing (CI, and any
# caller that sets MUTATION_FIXTURES_STRICT=1), and a loud, non-silent skip on
# a developer's dirty worktree, where failing would only train people to
# delete the fixture.
if [[ -n "$(git -C "$REPO_ROOT" status --porcelain)" ]]; then
  echo "go-vet-copylocks: CANNOT RUN — the worktree is dirty, and mutate.sh needs a" >&2
  echo "  clean tree for its post-restore check to mean anything. Commit or clean up, then:" >&2
  echo "    bash tools/lib/go-vet-copylocks_test.sh" >&2
  if [[ -n "${CI:-}" || -n "${MUTATION_FIXTURES_STRICT:-}" ]]; then
    echo "  (failing rather than skipping: CI/strict mode, where a skip is indistinguishable" >&2
    echo "   from a pass and this gate is the only thing re-running this mutation)" >&2
    exit 1
  fi
  echo "  (skipped locally; set MUTATION_FIXTURES_STRICT=1 to make this a failure)" >&2
  exit 0
fi

ANCHOR='const unresolvedTTL = 30 * time.Second
'
REPLACEMENT='const unresolvedTTL = 30 * time.Second

// copylocksViolationFixture is injected by
// tools/lib/go-vet-copylocks_test.sh (#1823) to prove `go vet` flags passing
// a lock by value where `go test` alone does not. Dead code, never called —
// a pure static vet finding, not a behavior change. tools/mutate.sh restores
// this file byte-for-byte immediately after the fixture runs.
func copylocksViolationFixture(t ConcurrencyTracker) { _ = t }
'

out="$(cd "$REPO_ROOT" && "$MUTATE_SH" "$TARGET_FILE" "$ANCHOR" "$REPLACEMENT" \
  bash -c "go vet $PKG; echo GO_VET_RC=\$?; go test $PKG -count=1; echo GO_TEST_RC=\$?" 2>&1)"
rc=$?

fails=0

# mutate.sh exits 0 when the mutation applied and the tree was restored,
# WHATEVER the inner commands did — their results are in the captured text.
# A non-zero rc is mutate.sh itself refusing (STALE anchor, dirty tree, ...),
# which is a fixture bug, not a finding about the guard.
if [[ $rc -ne 0 ]]; then
  echo "FAIL: go-vet-copylocks — mutate.sh refused (exit $rc). A STALE anchor means the"
  echo "      surrounding text moved and this fixture needs its anchor updated; it does"
  echo "      NOT mean the go vet gates are fine."
  echo "$out" | sed 's/^/      | /'
  fails=$((fails + 1))
else
  # go vet MUST go red, naming the copylocks violation specifically — not
  # merely a non-zero exit, which a compile error or a different vet check
  # could also produce and would read as success for the wrong reason.
  if ! grep -q 'GO_VET_RC=[1-9]' <<<"$out"; then
    echo "FAIL: go-vet-copylocks — go vet stayed GREEN (exit 0) under a copylocks violation;"
    echo "      the go vet (core) gate does not reach what it claims to cover."
    echo "$out" | sed 's/^/      | /'
    fails=$((fails + 1))
  elif ! grep -qF 'passes lock by value' <<<"$out" || ! grep -qF 'contains sync.Mutex' <<<"$out"; then
    echo "FAIL: go-vet-copylocks — go vet failed, but not with a copylocks message."
    echo "$out" | sed 's/^/      | /'
    fails=$((fails + 1))
  else
    echo "ok  go vet goes RED on a copylocks violation"
  fi

  # go test MUST stay green under the identical mutation — this is the other
  # half of the claim: the existing `-race` test suite does not reach this
  # class of defect at all, so `go vet` is not redundant with it.
  if ! grep -q 'GO_TEST_RC=0' <<<"$out"; then
    echo "FAIL: go-vet-copylocks — go test did NOT stay green under the same mutation, so"
    echo "      this fixture no longer demonstrates the contrast it exists to prove."
    echo "$out" | sed 's/^/      | /'
    fails=$((fails + 1))
  else
    echo "ok  go test stays GREEN on the same copylocks violation"
  fi
fi

if [[ $fails -gt 0 ]]; then
  echo "go-vet-copylocks: $fails FAILED"
  exit 1
fi
echo "go-vet-copylocks: ALL PASS"
