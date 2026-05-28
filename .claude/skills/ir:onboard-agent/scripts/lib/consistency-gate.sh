#!/usr/bin/env bash
# consistency-gate.sh — the assessment ⟺ scenarios agreement gate.
#
# Green means: for every un-recorded cell, the assessment.json verdict and
# scenarios.json's by_adapter.<agent>.applicable flag tell the SAME story. It is
# the SEMANTIC companion to catalog-drift.sh (structural bijection). It exists
# because the completeness-gate resolves disagreements SILENTLY — it marks a
# cell terminal the instant scenarios.json says applicable:false, without
# checking the assessment. That is exactly how pi/streaming-partial-writes
# carried daemon=full/driver=ready AND applicable:false at once, recorded by
# nobody, with every structural gate green (#507).
#
# #508 collapsed this gate into a THIN CLIENT of the canonical matrix model
# (tools/agent-onboarding/internal/matrix, exposed via `matrix query`). The
# routing matrix (frozen / record_now / driver_gap / inconclusive), the
# applicable-state rollup, the record_blocked exemption, and the per-error
# messages now live in Go — proven byte-for-byte equal to the prior bash via the
# package's parity tests (internal/matrix/matrix_test.go). This file only
# forwards the CLI arguments.
#
# CLI: consistency-gate.sh [scenarios.json] [agents-root]
#   exit 0 — assessment and scenarios agree on every un-recorded cell
#   exit 1 — at least one contradiction (listed on stderr)
#   exit 2 — usage / infra (no Go toolchain)
#
# Sourced as a library (for matrix_cli) AND runnable as a CLI. MUST NOT call
# `set` at top level (it would leak options into a sourcing shell).

_CS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=matrix-cli.sh
source "$_CS_DIR/matrix-cli.sh"

if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  set -uo pipefail
  SK="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"   # …/ir:onboard-agent
  REPO="$(cd "$SK/../../.." && pwd)"
  scenarios="${1:-$SK/scenarios.json}"
  root="${2:-$REPO/replaydata/agents}"

  matrix_cli --gate consistency --scenarios "$scenarios" --agents-root "$root"
  exit $?
fi
