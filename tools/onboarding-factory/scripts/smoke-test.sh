#!/usr/bin/env bash
# smoke-test.sh — fast, dependency-light checks for the recording-rig shell
# scripts. The rig is re-run at record time but is NOT exercised by
# replay-fixtures.sh / `go test` (those replay static transcripts without
# invoking the drivers), so without this the rig has zero automated coverage —
# which is exactly where a code review found three silent-failure bugs.
#
# Hard gates (fail the run):
#   1. bash -n syntax check on every *.sh under scripts/ (incl. lib/)
#   2. lib/reconcile_test.sh unit tests
# Advisory (printed, never fails — the rig predates shellcheck and may carry
# legacy warnings; tighten later if desired):
#   3. shellcheck -S warning, if installed
#
# Run directly:  tools/onboarding-factory/scripts/smoke-test.sh
set -uo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
rc=0

echo "== bash -n syntax check =="
while IFS= read -r f; do
  if err="$(bash -n "$f" 2>&1)"; then
    echo "  ok: ${f#"$SCRIPT_DIR"/}"
  else
    echo "  SYNTAX ERROR: ${f#"$SCRIPT_DIR"/}" >&2
    echo "$err" >&2
    rc=1
  fi
done < <(find "$SCRIPT_DIR" -maxdepth 3 -name '*.sh' -type f | sort)

echo ""
echo "== unit tests (lib/reconcile_test.sh) =="
bash "$SCRIPT_DIR/lib/reconcile_test.sh" || rc=1

echo ""
echo "== unit tests (lib/recipe-lint_test.sh) =="
bash "$SCRIPT_DIR/lib/recipe-lint_test.sh" || rc=1

echo ""
echo "== unit tests (lib/cell-integrity_test.sh) =="
bash "$SCRIPT_DIR/lib/cell-integrity_test.sh" || rc=1

echo ""
echo "== unit tests (lib/classify-failure_test.sh) =="
bash "$SCRIPT_DIR/lib/classify-failure_test.sh" || rc=1

echo ""
echo "== unit tests (lib/managed-file-snapshot_test.sh) =="
bash "$SCRIPT_DIR/lib/managed-file-snapshot_test.sh" || rc=1

echo ""
echo "== unit tests (lib/spawn-record-daemon_test.sh) =="
bash "$SCRIPT_DIR/lib/spawn-record-daemon_test.sh" || rc=1

echo ""
echo "== unit tests (lib/completeness-check_test.sh) =="
bash "$SCRIPT_DIR/lib/completeness-check_test.sh" || rc=1

echo ""
echo "== unit tests (lib/pick-recording_test.sh) =="
bash "$SCRIPT_DIR/lib/pick-recording_test.sh" || rc=1

echo ""
echo "== unit tests (lib/atomic-promote_test.sh) =="
bash "$SCRIPT_DIR/lib/atomic-promote_test.sh" || rc=1

echo ""
echo "== unit tests (lib/promote-hookcheck_test.sh) =="
bash "$SCRIPT_DIR/lib/promote-hookcheck_test.sh" || rc=1

echo ""
echo "== unit tests (lib/unapplied-grants-check_test.sh) =="
bash "$SCRIPT_DIR/lib/unapplied-grants-check_test.sh" || rc=1

echo ""
echo "== unit tests (lib/adapter-tables_test.sh) =="
bash "$SCRIPT_DIR/lib/adapter-tables_test.sh" || rc=1

echo ""
echo "== unit tests (lib/golden-scope_test.sh) =="
bash "$SCRIPT_DIR/lib/golden-scope_test.sh" || rc=1

echo ""
echo "== unit tests (lib/agent-home_test.sh) =="
bash "$SCRIPT_DIR/lib/agent-home_test.sh" || rc=1

# completeness-gate / catalog-drift / consistency gates were retired (#528):
# `of validate` + `of coverage` (Go) now own schema + referential + coverage
# integrity, and a per-scenario shard is the single source for a cell, so the
# catalog↔rollup bijection those bash gates enforced can no longer drift.

echo ""
echo "== unit tests (replaydata/_lib/drive/drive-lib_test.sh) =="
bash "$SCRIPT_DIR/../../../replaydata/_lib/drive/drive-lib_test.sh" || rc=1

echo ""
echo "== unit tests (replaydata/_lib/drive/turn-count_test.sh) =="
bash "$SCRIPT_DIR/../../../replaydata/_lib/drive/turn-count_test.sh" || rc=1

# Adapter-local driver libs. codex's boot gates are the only ones with a corpus
# today (#1388) — the strings its interactive driver must recognize before codex
# will accept a prompt. They earn a suite because the failure mode of a string
# that stops matching is silent: the gate stays on screen, the prompt is typed
# into it, and the run records a healthy-looking fixture that fires zero hooks.
echo ""
echo "== unit tests (replaydata/agents/codex/boot-gates_test.sh) =="
bash "$SCRIPT_DIR/../../../replaydata/agents/codex/boot-gates_test.sh" || rc=1

echo ""
# The advisory shellcheck pass that used to live here is gone (#1684). It was
# the shape AGENTS.md rules out: `|| true` per file so findings never moved rc,
# a trailing `echo` so the block always exited 0, and a silent
# `(shellcheck not installed — skipped)` on a runner that ships none — so
# "nothing was found" and "nothing looked" printed the same verdict, and the
# nine real findings in this tree (SC1087, SC2155, and seven SC2034) sat in a
# 100-line log for as long as it existed.
#
# `tools/bash-lint.sh` now covers this whole tree as a REAL gate: it refuses
# rather than skipping when shellcheck is absent, lints one file at a time (a
# multi-file invocation cross-suppresses SC2034), and runs in linux.yml and
# `tools/preflight.sh --only bash`. Two mechanisms where one of them could not
# fail is worse than one that can.
#
# Note the scope that sentence did NOT cover until #1687, because it is the
# tree this file's own suites live in: `replaydata/**` was a declared exclusion
# of that gate, so the three driver-lib suites run above — and every per-agent
# driver they exercise — were read by no static analyser at all, while the
# `bash -n` pass at the top of this file walks only `scripts/`. #1687 removed
# the exclusion, so the deferral above is now true of the recording rig as
# well as of the factory scripts.
echo "== shellcheck =="
echo "  (covered by tools/bash-lint.sh — linux.yml, or tools/preflight.sh --only bash)"

echo ""
if [[ $rc -eq 0 ]]; then
  echo "smoke-test: PASS"
else
  echo "smoke-test: FAIL" >&2
fi
exit $rc
