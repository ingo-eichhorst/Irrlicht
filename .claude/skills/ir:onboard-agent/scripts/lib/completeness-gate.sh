#!/usr/bin/env bash
# completeness-gate.sh — post-sweep forcing function (#496 RC4): assert that
# every coverage_id APPLICABLE to an agent reached a TERMINAL verdict, so a
# cell can never silently fall through a sweep.
#
# Terminal verdicts (pass): recorded | applicable_false | driver_gap.
# Non-terminal (FAIL):       unassessed | assessed_not_recorded.
#
# #508 collapsed this gate into a THIN CLIENT of the canonical matrix model
# (tools/agent-onboarding/internal/matrix, exposed via `matrix query`). The
# applicability rule (`requires` vs capabilities.json + `requires_transport`),
# the per-cell disposition decision table, and the table/summary output all now
# live in Go — proven byte-for-byte equal to the prior bash via the package's
# parity tests (internal/matrix/matrix_test.go). This file only translates the
# CLI arguments and forwards them.
#
# CLI: completeness-gate.sh <agent> [scenarios.json] [replaydata-root]
#   exit 0 — every applicable coverage_id is terminal
#   exit 1 — one or more non-terminal cells (listed on stderr with next action)
#   exit 2 — usage / infra (missing capabilities.json, no Go toolchain)
#
# Sourced as a library (for matrix_cli) AND runnable as a CLI. MUST NOT call
# `set` at top level (it would leak options into a sourcing shell).

_CG_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=matrix-cli.sh
source "$_CG_DIR/matrix-cli.sh"

if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  set -uo pipefail
  agent="${1:?usage: completeness-gate.sh <agent> [scenarios.json] [replaydata-root]}"
  SK="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"   # …/ir:onboard-agent
  REPO="$(cd "$SK/../../.." && pwd)"                          # repo root
  scenarios="${2:-$SK/scenarios.json}"
  root="${3:-$REPO/replaydata}"

  matrix_cli --gate completeness --agent "$agent" \
    --scenarios "$scenarios" --agents-root "$root/agents"
  exit $?
fi
