#!/usr/bin/env bash
# pick-recording_test.sh — unit tests for pick-recording.sh. Plain bash (no
# framework), matching the style of classify-failure_test.sh. Run directly or via
# scripts/smoke-test.sh.
#
# Why this exists (#1333, finding B6). run-cell.sh's attach path asked whether
# ANY .jsonl existed in the attached daemon's recordings dir, then — when nothing
# was newer than the attach marker — "fell back to the most-recent file
# regardless of mtime". But "nothing is newer than the marker" is PRECISELY the
# signal that the attached daemon is not recording this run. The fallback threw
# that detection away and curated whatever ran last; the hermes onboarding run
# was handed a recording from the previous day and told it had succeeded.
#
# The guard now proves FRESHNESS, not presence.

set -uo pipefail   # NOT -e: assertions capture non-zero return codes

DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=pick-recording.sh
source "$DIR/pick-recording.sh"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

fails=0
pass() { local label="$1"; echo "  PASS: $label"; return 0; }
fail() { local label="$1" expected="$2" got="$3"; echo "  FAIL: $label — expected [$expected] got [$got]"; fails=$((fails + 1)); return 0; }

assert_eq() {
  local label="$1" expected="$2" got="$3"
  [[ "$got" == "$expected" ]] && pass "$label" || fail "$label" "$expected" "$got"
  return 0
}
assert_contains() {
  local label="$1" needle="$2" hay="$3"
  [[ "$hay" == *"$needle"* ]] && pass "$label" || fail "$label" "*$needle*" "$hay"
  return 0
}

echo "== a recording written during the run is picked =="
d="$TMP/fresh"; mkdir -p "$d"
touch -t 202601010000 "$d/old-session.jsonl"
marker="$TMP/fresh.marker"; : > "$marker"
sleep 1
: > "$d/new-session.jsonl"
out="$(pick_attached_recording "$d" '*.jsonl' "$marker" 2>/dev/null)"
rc=$?
assert_eq "returns 0" "0" "$rc"
assert_eq "picks the fresh file" "$d/new-session.jsonl" "$out"

echo "== nothing newer than the marker FAILS (it must not curate a stale file) =="
d="$TMP/stale"; mkdir -p "$d"
touch -t 202601010000 "$d/yesterday.jsonl"
marker="$TMP/stale.marker"; : > "$marker"
err="$(pick_attached_recording "$d" '*.jsonl' "$marker" 2>&1 >/dev/null)"
rc=$?
assert_eq "returns non-zero" "1" "$rc"
assert_contains "diagnoses the non-recording daemon" "not recording" "$err"
assert_contains "names the dir it looked in" "$d" "$err"

echo "== an empty recordings dir fails the same way =="
d="$TMP/empty"; mkdir -p "$d"
marker="$TMP/empty.marker"; : > "$marker"
err="$(pick_attached_recording "$d" '*.jsonl' "$marker" 2>&1 >/dev/null)"
rc=$?
assert_eq "returns non-zero" "1" "$rc"
assert_contains "diagnoses the non-recording daemon" "not recording" "$err"

echo "== the newest of several fresh recordings wins (daemon rotated mid-run) =="
d="$TMP/rotated"; mkdir -p "$d"
marker="$TMP/rotated.marker"; : > "$marker"
sleep 1
: > "$d/a-first.jsonl"
: > "$d/b-second.jsonl"
out="$(pick_attached_recording "$d" '*.jsonl' "$marker" 2>/dev/null)"
assert_eq "picks the last by sort order" "$d/b-second.jsonl" "$out"

echo "== isolated mode is unaffected: first match in the staging dir =="
d="$TMP/isolated"; mkdir -p "$d"
: > "$d/only.jsonl"
out="$(pick_isolated_recording "$d" '*.jsonl' 2>/dev/null)"
assert_eq "picks the staged recording" "$d/only.jsonl" "$out"

d="$TMP/isolated-empty"; mkdir -p "$d"
out="$(pick_isolated_recording "$d" '*.jsonl' 2>/dev/null)"
rc=$?
assert_eq "empty staging dir returns non-zero" "1" "$rc"

echo ""
if [[ "$fails" -eq 0 ]]; then
  echo "pick-recording_test: ALL PASS"
else
  echo "pick-recording_test: $fails FAILED" >&2
  exit 1
fi
