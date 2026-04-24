#!/usr/bin/env bash
# precheck.sh — fail-fast correctness bundle for the ir:onboard-agent skill.
#
# Every check prints a specific failing-check name on stderr and exits
# nonzero so the skill can surface the exact reason. These checks are for
# correctness (port-clash, fixture-tree cleanliness, CLI compat) — they
# assume the agent CLI itself is already authenticated/subscribed by the
# user; auth failures surface through the CLI's own stderr.
#
# Usage:
#   precheck.sh <adapter> <scenarios-json>
#
#   adapter: claudecode | codex | pi (the adapter whose CLI version
#            will be checked against min_versions in scenarios.json)

set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: precheck.sh <adapter> <scenarios-json>" >&2
  exit 2
fi

ADAPTER="$1"
SCENARIOS_JSON="$2"

fail() {
  echo "precheck: $*" >&2
  exit 1
}

# 1. Adapter is supported by a driver.
case "$ADAPTER" in
  claudecode) ;;
  codex|pi)
    fail "adapter '$ADAPTER' has no driver script yet (see README: 'Adding an adapter column')"
    ;;
  *)
    fail "unknown adapter: $ADAPTER"
    ;;
esac

# 2. No production daemon running — the isolated daemon we launch binds
#    port 7837, and so does the user's production irrlichd. Racing them
#    would route hooks to the wrong process.
if pgrep -x irrlichd >/dev/null 2>&1; then
  fail "another irrlichd is running (pgrep -x irrlichd); stop it first"
fi

# 3. Clean working tree under testdata/replay/. A dirty tree means the
#    maintainer already has staged fixture changes; we refuse to layer
#    another round on top.
REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || true)"
if [[ -z "$REPO_ROOT" ]]; then
  fail "not in a git repository"
fi
if [[ -n "$(cd "$REPO_ROOT" && git status --porcelain testdata/replay/ 2>/dev/null)" ]]; then
  fail "testdata/replay/ has uncommitted changes; commit or stash first"
fi

# 4. Adapter CLI present + version check against min_versions in scenarios.json.
if ! command -v jq >/dev/null 2>&1; then
  fail "jq is required (brew install jq)"
fi
if [[ ! -f "$SCENARIOS_JSON" ]]; then
  fail "scenarios.json not found at $SCENARIOS_JSON"
fi
MIN_VERSION="$(jq -r --arg a "$ADAPTER" '.min_versions[$a] // empty' "$SCENARIOS_JSON")"

case "$ADAPTER" in
  claudecode)
    if ! command -v claude >/dev/null 2>&1; then
      fail "claude CLI not on PATH"
    fi
    CLAUDE_VER="$(claude --version 2>/dev/null | awk '{print $1}' | head -n1)"
    if [[ -z "$CLAUDE_VER" ]]; then
      fail "could not parse 'claude --version' output"
    fi
    if [[ -n "$MIN_VERSION" ]]; then
      LOWEST="$(printf '%s\n%s\n' "$MIN_VERSION" "$CLAUDE_VER" | sort -V | head -n1)"
      if [[ "$LOWEST" != "$MIN_VERSION" ]]; then
        fail "claude $CLAUDE_VER is below pinned minimum $MIN_VERSION"
      fi
    fi
    ;;
esac

# 5. Build irrlichd + replay from the current worktree so recordings
#    reflect code under review, and so run-cell.sh can invoke replay
#    directly without paying a `go run` recompile per cell.
BIN_DIR="$REPO_ROOT/.build/refresh/bin"
mkdir -p "$BIN_DIR"
for bin in irrlichd replay; do
  if ! (cd "$REPO_ROOT" && go build -o "$BIN_DIR/$bin" ./core/cmd/"$bin") >/dev/null 2>&1; then
    fail "failed to build $bin from ./core/cmd/$bin"
  fi
done

echo "precheck: OK (adapter=$ADAPTER, claude=${CLAUDE_VER:-n/a}, bin=$BIN_DIR)"
