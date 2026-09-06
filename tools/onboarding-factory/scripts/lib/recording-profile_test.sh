#!/usr/bin/env bash
# recording-profile_test.sh — selection and input-contract tests.

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=recording-profile.sh
source "$DIR/recording-profile.sh"

command -v jq >/dev/null || { echo "recording-profile_test: jq is required" >&2; exit 2; }

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
fails=0
pass() { echo "  PASS: $1"; }
fail() { echo "  FAIL: $1 — expected [$2] got [$3]"; fails=$((fails + 1)); }
assert_eq() { [[ "$2" == "$3" ]] && pass "$1" || fail "$1" "$2" "$3"; }
assert_file_contains() {
  grep -Fq -- "$2" "$3" && pass "$1" || fail "$1" "$2" "$3"
}

CELL="$TMP/cell"
mkdir -p "$CELL/recordings/r1" "$CELL/recordings/r2" "$CELL/recordings/r3"
printf '%s\n' '{"execution_profile":"cli-local"}' > "$CELL/recordings/r1/manifest.json"
printf '%s\n' '{"execution_profile":"desktop-local"}' > "$CELL/recordings/r2/manifest.json"
printf '%s\n' '{"execution_profile":"desktop-local"}' > "$CELL/recordings/r3/manifest.json"

echo "== selection filters before newest =="
assert_eq "newest CLI ignores newer Desktop" "$CELL/recordings/r1" \
  "$(newest_recording_for_profile "$CELL" cli-local)"
assert_eq "newest Desktop" "$CELL/recordings/r3" \
  "$(newest_recording_for_profile "$CELL" desktop-local)"

echo "== only an absent field defaults to CLI =="
printf '%s\n' '{}' > "$CELL/recordings/r3/manifest.json"
assert_eq "absent field is CLI" cli-local "$(recording_execution_profile "$CELL/recordings/r3")"
for value in '""' null; do
  printf '{"execution_profile":%s}\n' "$value" > "$CELL/recordings/r3/manifest.json"
  recording_execution_profile "$CELL/recordings/r3" >/dev/null 2>&1
  assert_eq "present $value fails" 2 "$?"
done

echo "== selector fails on an unknown profile =="
newest_recording_for_profile "$CELL" remote >/dev/null 2>&1
assert_eq "unknown requested profile" 2 "$?"

echo "== current-recording consumers use the shared selector =="
assert_file_contains "replay gate selects by profile" \
  'newest_recording_for_profile "$cell_dir" "$EXECUTION_PROFILE"' "$DIR/../../../replay-fixtures.sh"
assert_file_contains "runner selects evidence for its requested profile" \
  'newest_recording_for_profile "$COMMITTED_CELL" "$EXECUTION_PROFILE"' "$DIR/../run-cell.sh"
assert_file_contains "integrity gate selects CLI evidence" \
  'newest_recording_for_profile "$dir" cli-local' "$DIR/cell-integrity.sh"

echo ""
if [[ "$fails" -eq 0 ]]; then
  echo "recording-profile_test: ALL PASS"
else
  echo "recording-profile_test: $fails FAILED" >&2
  exit 1
fi
