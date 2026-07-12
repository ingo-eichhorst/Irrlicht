#!/usr/bin/env bash
# reconcile_test.sh — unit tests for lib/reconcile.sh. No test framework
# (plain bash + jq, which the lib needs anyway). Run directly or via
# scripts/smoke-test.sh. Exits non-zero on any failed assertion.
#
# Covers the three silent-failure modes a code review found in the
# cross-adapter reconciliation (and that have no other automated coverage,
# since replay-fixtures replays static transcripts without invoking the rig):
#   - adapter-pinned lookup + fallback        (daemon_sid_for_transcript)
#   - reconciled-id presence guard            (sid_in_recording)
#   - uuid<->path lockstep alignment          (reconcile_slot_csv)

set -uo pipefail   # NOT -e: assertions capture non-zero return codes

DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=reconcile.sh
source "$DIR/reconcile.sh"

command -v jq >/dev/null || { echo "reconcile_test: jq is required" >&2; exit 2; }

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# Synthetic recording: pi keys /P1../P3 → S1..S3; claudecode's daemon name is
# "claude-code" (not the "claudecode" slug) with session_id == its UUID.
export RECORDING="$TMP/rec.jsonl"
cat > "$RECORDING" <<'JSON'
{"kind":"transcript_new","adapter":"pi","transcript_path":"/P1","session_id":"S1","seq":1}
{"kind":"transcript_new","adapter":"pi","transcript_path":"/P2","session_id":"S2","seq":2}
{"kind":"transcript_new","adapter":"pi","transcript_path":"/P3","session_id":"S3","seq":3}
{"kind":"transcript_new","adapter":"claude-code","transcript_path":"/CC","session_id":"CCUUID","seq":4}
{"kind":"state_transition","session_id":"S1","seq":5}
JSON

fails=0
pass() { local label="$1"; echo "  PASS: $label"; return 0; }
fail() { local label="$1" expected="$2" got="$3"; echo "  FAIL: $label — expected [$expected] got [$got]"; fails=$((fails + 1)); return 0; }
assert_eq() { local label="$1" expected="$2" actual="$3"; [[ "$expected" == "$actual" ]] && pass "$label" || fail "$label" "$expected" "$actual"; return 0; }

echo "== daemon_sid_for_transcript: adapter-pinned lookup + fallback =="
assert_eq "path+adapter match → daemon stem" \
  "S1" "$(daemon_sid_for_transcript /P1 pi FB)"
assert_eq "wrong adapter → fallback (pin excludes)" \
  "FB" "$(daemon_sid_for_transcript /P1 codex FB)"
assert_eq "claudecode slug misses 'claude-code' → fallback UUID kept" \
  "CCUUID" "$(daemon_sid_for_transcript /CC claudecode CCUUID)"
assert_eq "no transcript_new for path → fallback" \
  "FB" "$(daemon_sid_for_transcript /nope pi FB)"
assert_eq "empty path → fallback" \
  "FB" "$(daemon_sid_for_transcript '' pi FB)"

echo "== sid_in_recording: reconciled-id presence guard =="
sid_in_recording S1     ; assert_eq "present id → rc 0"        "0" "$?"
sid_in_recording CCUUID ; assert_eq "present (claude-code) → 0" "0" "$?"
sid_in_recording NOPE   ; assert_eq "absent id → rc 1 (caught)" "1" "$?"
sid_in_recording ''     ; assert_eq "empty id → rc 1"          "1" "$?"

echo "== reconcile_slot_csv: uuid<->path lockstep alignment =="
printf 'U1\nU2\n'      > "$TMP/u_ok";  printf '/P1\n/P2\n'     > "$TMP/p_ok"
printf 'U1\n\nU3\n'    > "$TMP/u_eu";  printf '/P1\n/P2\n/P3\n'> "$TMP/p_eu"
printf 'U1\nU2\nU3\n'  > "$TMP/u_ep";  printf '/P1\n\n/P3\n'   > "$TMP/p_ep"

assert_eq "all slots present → both reconciled" \
  "$(printf 'S1\nS2')" "$(reconcile_slot_csv "$TMP/u_ok" "$TMP/p_ok" pi)"
# The regression test: empty MIDDLE uuid must NOT shift U3 onto /P2.
assert_eq "empty middle uuid slot → U3 stays paired with /P3 (not /P2)" \
  "$(printf 'S1\nS3')" "$(reconcile_slot_csv "$TMP/u_eu" "$TMP/p_eu" pi)"
# Empty middle PATH (claudecode-style: minted uuid, unresolved transcript):
# U2 falls back to its own id, U3 still pairs with /P3.
assert_eq "empty middle path slot → U2 fallback, U3 paired with /P3" \
  "$(printf 'S1\nU2\nS3')" "$(reconcile_slot_csv "$TMP/u_ep" "$TMP/p_ep" pi)"

echo ""
if [[ "$fails" -eq 0 ]]; then
  echo "reconcile_test: ALL PASS"
else
  echo "reconcile_test: $fails FAILED" >&2
  exit 1
fi
