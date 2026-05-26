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
# Run directly:  .claude/skills/ir:onboard-agent/scripts/smoke-test.sh
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
done < <(find "$SCRIPT_DIR" -maxdepth 2 -name '*.sh' -type f | sort)

echo ""
echo "== unit tests (lib/reconcile_test.sh) =="
bash "$SCRIPT_DIR/lib/reconcile_test.sh" || rc=1

echo ""
echo "== unit tests (lib/recipe-lint_test.sh) =="
bash "$SCRIPT_DIR/lib/recipe-lint_test.sh" || rc=1

echo ""
echo "== unit tests (lib/completeness-gate_test.sh) =="
bash "$SCRIPT_DIR/lib/completeness-gate_test.sh" || rc=1

echo ""
echo "== shellcheck (advisory) =="
if command -v shellcheck >/dev/null 2>&1; then
  # -x follows `source`d libs; advisory only — does not change rc.
  find "$SCRIPT_DIR" -maxdepth 2 -name '*.sh' -type f | sort | while IFS= read -r f; do
    shellcheck -S warning -x "$f" || true
  done
  echo "  (advisory — see findings above, if any)"
else
  echo "  (shellcheck not installed — skipped)"
fi

echo ""
if [[ $rc -eq 0 ]]; then
  echo "smoke-test: PASS"
else
  echo "smoke-test: FAIL" >&2
fi
exit $rc
