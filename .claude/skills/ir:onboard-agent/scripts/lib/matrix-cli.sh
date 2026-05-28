#!/usr/bin/env bash
# matrix-cli.sh — locate/build the `matrix` query binary and run it.
#
# #508 introduced internal/matrix/ as the single canonical model of the
# scenario × adapter matrix, with a `matrix query` CLI (tools/agent-onboarding/
# cmd/matrix). The completeness and consistency gates — which used to each
# reconstruct the matrix from scenarios.json + capabilities.json + on-disk
# artifacts with their own jq filters — are now THIN CLIENTS of that binary.
# This helper is the shared shim they source.
#
# Exit-code fidelity matters: the gates distinguish exit 2 (infra/usage) from
# exit 1 (gate failure). `go run` collapses every non-zero program exit to 1,
# so we build a real binary (cached under .build/) and exec it. Set
# IR_MATRIX_BIN to a prebuilt binary to skip the build entirely.
#
# Sourced as a library; MUST NOT call `set` at top level (it would leak options
# into a sourcing shell).

# matrix_repo_root → the repo root, derived from this file's location
# (.claude/skills/ir:onboard-agent/scripts/lib → up 5).
matrix_repo_root() {
  cd "$(dirname "${BASH_SOURCE[0]}")/../../../../.." && pwd
}

# matrix_cli <args...> → run `matrix query <args...>`, preserving its exit code.
#   exit 2 — toolchain/build problem (or the CLI's own usage/infra exit 2)
#
# For the hot path (sweeps, parallel runs) set IR_MATRIX_BIN to a prebuilt
# binary — then no per-call build happens at all. Otherwise we build to a
# PROCESS-UNIQUE temp file (not a shared .build/matrix), so two concurrent gate
# calls can't race the same `-o` target and exec a half-linked binary; and the
# compiler's stderr is surfaced on failure rather than swallowed.
matrix_cli() {
  if [[ -n "${IR_MATRIX_BIN:-}" && -x "${IR_MATRIX_BIN}" ]]; then
    "${IR_MATRIX_BIN}" query "$@"
    return $?
  fi
  command -v go >/dev/null 2>&1 || {
    echo "matrix-cli: the Go toolchain is required (or set IR_MATRIX_BIN to a prebuilt matrix binary)" >&2
    return 2
  }
  local repo bin build_err rc
  repo="$(matrix_repo_root)"
  bin="$(mktemp "${TMPDIR:-/tmp}/irrlicht-matrix.XXXXXX")" || {
    echo "matrix-cli: mktemp failed" >&2
    return 2
  }
  if ! build_err="$( (cd "$repo/tools/agent-onboarding" && go build -o "$bin" ./cmd/matrix) 2>&1 )"; then
    echo "matrix-cli: failed to build the matrix binary:" >&2
    echo "$build_err" >&2
    rm -f "$bin"
    return 2
  fi
  "$bin" query "$@"
  rc=$?
  rm -f "$bin"
  return "$rc"
}
