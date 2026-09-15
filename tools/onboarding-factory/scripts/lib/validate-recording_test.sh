#!/usr/bin/env bash
# validate-recording_test.sh — exercise promote-recording.sh's
# validate_recording() in isolation, faking `go run
# ./tools/onboarding-factory/cmd/expected-validate` via a PATH-shadowing stub
# so this never shells out to a real go build. Follows the same
# extract-a-marked-block-and-eval convention as
# recording-profile-manifest_test.sh (identity/population/version-chain/
# crosscheck), applied to the ONE function this test needs.
#
# WHY THIS EXISTS (#1967 QA finding). validate_recording()'s failure path was:
#
#   echo "$out" | jq -r '.summary' 2>/dev/null || echo "validate-failed"
#
# which looks like it falls back to the "validate-failed" sentinel whenever
# jq can't produce a summary. It doesn't: jq exits 0 on EMPTY stdin (zero
# JSON inputs is not an error to jq — measured: `printf '' | jq -r
# '.summary'` prints nothing and exits 0), so the `||` arm never fires when
# $out is empty. expected-validate's own internal-error path (exit 2 — e.g. a
# malformed expected.jsonl meta line) writes NOTHING to stdout, which is
# exactly that empty-$out shape. A verification mechanism must fail loudly
# when it cannot run (AGENTS.md); before the fix this stamped the SAME empty
# value for "the validator ran and said nothing" as for "the validator
# exploded before grading anything" — worst on a known_failing:true candidate
# (#1967), where atomic_promote's rc=2 path promotes on the validator's exit
# status alone and doesn't require a non-empty summary to do it.

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$DIR/../../../.." && pwd)"
PROMOTE="$ROOT/tools/promote-recording.sh"

fails=0
pass() { echo "  PASS: $1"; }
fail() { echo "  FAIL: $1 — $2"; fails=$((fails + 1)); }

extract_block() {
  local name="$1"
  awk -v begin="# BEGIN $name" -v end="# END $name" '
    $0 == begin { starts++; inside=1; next }
    $0 == end   { ends++; inside=0; next }
    inside      { print }
    END { if (starts != 1 || ends != 1) exit 42 }
  ' "$PROMOTE"
}

subject_block="$(extract_block validate_recording)"
subject_rc=$?
if [[ "$subject_rc" -ne 0 || -z "$subject_block" ]]; then
  fail "validate_recording extracted" "marker count changed or the block was empty"
  echo "validate-recording_test: $fails FAILED" >&2
  exit 1
fi
pass "validate_recording extracted"
eval "$subject_block"

# A fake `go` ahead of the real one on PATH, so `go run
# ./tools/onboarding-factory/cmd/expected-validate ...` never actually
# compiles or runs anything — this test controls expected-validate's exact
# stdout/exit shape instead of depending on a real cell + recording fixture.
STUB_DIR="$(mktemp -d "${TMPDIR:-/tmp}/irr-go-stub.XXXXXX")"
trap 'rm -rf "$STUB_DIR"' EXIT
cat > "$STUB_DIR/go" <<'STUB'
#!/usr/bin/env bash
# fake go: only understands `go run ./tools/onboarding-factory/cmd/expected-validate ...`
if [[ "${1:-}" == "run" ]]; then
  case "${GO_STUB_MODE:-}" in
    internal-error) exit 2 ;;                                   # nothing on stdout — #1967's exact shape
    fails)   echo '{"pass":false,"summary":"3/4 phases"}'; exit 1 ;;
    passes)  echo '{"pass":true,"summary":"4/4 phases"}';  exit 0 ;;
    *) echo "fake go: unrecognised GO_STUB_MODE '${GO_STUB_MODE:-}'" >&2; exit 99 ;;
  esac
fi
echo "fake go: unrecognised argv: $*" >&2
exit 127
STUB
chmod +x "$STUB_DIR/go"

# run_validate <mode> — a fresh subshell so the PATH/REPO_ROOT/
# EXECUTION_PROFILE/GO_STUB_MODE overrides here never leak to the next call.
run_validate() (
  PATH="$STUB_DIR:$PATH"
  REPO_ROOT="$ROOT"
  EXECUTION_PROFILE="cli-local"
  GO_STUB_MODE="$1"
  export PATH REPO_ROOT EXECUTION_PROFILE GO_STUB_MODE
  validate_recording "$ROOT" "irrelevant-recording-name"
)

echo "== the validator's internal-error case: empty stdout must reach the sentinel, not the empty string =="
out="$(run_validate internal-error)"; rc=$?
[[ "$out" == "validate-failed" ]] && pass "internal error -> 'validate-failed' sentinel" \
  || fail "internal error -> 'validate-failed' sentinel" "got [$out]"
[[ "$rc" -eq 1 ]] && pass "internal error -> exit 1" || fail "internal error -> exit 1" "got [$rc]"

echo "== a genuine validation failure still reports the real summary, not the sentinel =="
out="$(run_validate fails)"; rc=$?
[[ "$out" == "3/4 phases" ]] && pass "validation failure -> real summary" \
  || fail "validation failure -> real summary" "got [$out]"
[[ "$rc" -eq 1 ]] && pass "validation failure -> exit 1" || fail "validation failure -> exit 1" "got [$rc]"

echo "== a clean pass still reports its summary =="
out="$(run_validate passes)"; rc=$?
[[ "$out" == "4/4 phases" ]] && pass "clean pass -> real summary" \
  || fail "clean pass -> real summary" "got [$out]"
[[ "$rc" -eq 0 ]] && pass "clean pass -> exit 0" || fail "clean pass -> exit 0" "got [$rc]"

echo ""
if [[ "$fails" -eq 0 ]]; then
  echo "validate-recording_test: ALL PASS"
else
  echo "validate-recording_test: $fails FAILED" >&2
  exit 1
fi
