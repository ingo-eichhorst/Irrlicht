#!/usr/bin/env bash
# ars-gate.sh — deterministic architecture-regression check (advisory, not a
# merge gate: flags a regression for review, doesn't block a PR on its own).
#
# Scans the given directory (default: core) with ARS (Agent Readiness Score,
# github.com/ingo-eichhorst/agent-readyness) and fails if the composite score,
# or any individual category score, has regressed vs a fresh origin/main scan.
#
# Shared by tools/preflight.sh's `arch` gate and .github/workflows/ars-gate.yml
# so both surfaces use identical comparison logic — never duplicate the diff
# logic in preflight.sh or the workflow, call this script from both.
#
# Usage: tools/ars-gate.sh [<dir>]   # <dir> is relative to the repo root, default "core"
set -euo pipefail

DIR="${1:-core}"
TOLERANCE="0.05"
ARS_VERSION="v0.0.9" # keep in sync with .github/workflows/ars.yml

# `go install` writes to `go env GOBIN`, or else to the first `go env GOPATH`
# entry's bin/. Nothing guarantees that directory is on PATH: actions/setup-go
# adds it in CI, but a shell that lacked it got `ars: command not found`
# straight after the install below (#2099). Append it rather than prepend, so an
# `ars` already on PATH keeps precedence. Only consult `go` once ars is
# missing, so a machine with ars on PATH and no Go toolchain still runs.
# tools/security-scan.sh resolves govulncheck and gosec the same way.
# tools/lib/ars-gate_test.sh drives each of these branches.
if ! command -v ars >/dev/null 2>&1; then
  GO_BIN_DIR=$(go env GOBIN)
  if [[ -z "$GO_BIN_DIR" ]]; then
    GO_PATH_LIST=$(go env GOPATH)
    GO_BIN_DIR="${GO_PATH_LIST%%:*}/bin"
  fi
  PATH="$PATH:$GO_BIN_DIR"
  command -v ars >/dev/null 2>&1 || go install "github.com/ingo-eichhorst/agent-readyness/cmd/ars@${ARS_VERSION}"
  command -v ars >/dev/null 2>&1 || {
    echo "ars-gate: installed ars, but cannot find it in $GO_BIN_DIR or on PATH — refusing to scan" >&2
    exit 1
  }
fi

REPO_ROOT=$(git rev-parse --show-toplevel)
cd "$REPO_ROOT"

AFTER_JSON=$(mktemp)
BEFORE_JSON=$(mktemp)
BASE_WORKTREE=$(mktemp -d)
cleanup() {
  git worktree remove "$BASE_WORKTREE" --force >/dev/null 2>&1 || true
  rm -f "$AFTER_JSON" "$BEFORE_JSON"
}
trap cleanup EXIT

# `ars scan --json` prints a diagnostic line ("LLM features disabled...")
# to stdout before the JSON payload; strip anything before the first "{".
echo "ars-gate: scanning current tree ($DIR)..." >&2
ars scan "$DIR" --no-llm --json | sed -n '/^{/,$p' >"$AFTER_JSON"

echo "ars-gate: fetching origin/main and scanning baseline..." >&2
git fetch --quiet origin main
git worktree add --quiet --detach "$BASE_WORKTREE" origin/main
ars scan "$BASE_WORKTREE/$DIR" --no-llm --json | sed -n '/^{/,$p' >"$BEFORE_JSON"

RESULT=$(jq -n \
  --slurpfile before "$BEFORE_JSON" \
  --slurpfile after "$AFTER_JSON" \
  --argjson tol "$TOLERANCE" \
  '
  ($before[0]) as $b | ($after[0]) as $a |
  (($b.categories // []) | map({(.name): .score}) | add // {}) as $bc |
  (($a.categories // []) | map({(.name): .score}) | add // {}) as $ac |
  {
    composite_before: $b.composite_score,
    composite_after: $a.composite_score,
    composite_regressed: (($b.composite_score - $a.composite_score) > $tol),
    category_regressions: [
      $bc | keys[] | . as $k
      | select($ac[$k] != null and ($bc[$k] - $ac[$k]) > $tol)
      | {name: $k, before: $bc[$k], after: $ac[$k], delta: ($ac[$k] - $bc[$k])}
    ]
  }
  ')

echo "$RESULT" | jq .

COMPOSITE_REGRESSED=$(echo "$RESULT" | jq -r '.composite_regressed')
CATEGORY_REGRESSION_COUNT=$(echo "$RESULT" | jq '.category_regressions | length')

if [[ "$COMPOSITE_REGRESSED" = "true" ]] || [[ "$CATEGORY_REGRESSION_COUNT" -gt 0 ]]; then
  echo "ars-gate: FAILED — architecture health regressed vs origin/main" >&2
  echo "$RESULT" | jq '.category_regressions' >&2
  exit 1
fi

echo "ars-gate: passed (composite $(echo "$RESULT" | jq -r '.composite_before') -> $(echo "$RESULT" | jq -r '.composite_after'))" >&2
