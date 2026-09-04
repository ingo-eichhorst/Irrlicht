#!/usr/bin/env bash
# recording-profile-manifest_test.sh — exercise the manifest identity block
# from promote-recording.sh without performing a live promotion.

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

identity_block="$(extract_block recording_identity)"
identity_rc=$?
population_block="$(extract_block recording_manifest_population)"
population_rc=$?
if [[ "$identity_rc" -ne 0 || "$population_rc" -ne 0 || -z "$identity_block" || -z "$population_block" ]]; then
  fail "manifest test extracted its subjects" "marker count changed or a block was empty"
  echo "recording-profile-manifest_test: $fails FAILED" >&2
  exit 1
fi
pass "manifest test extracted its subjects"

check_manifest() (
  local profile="$1" transcript_entrypoint="$2" desktop_version="$3"
  local tmp staged dst
  tmp="$(mktemp -d "${TMPDIR:-/tmp}/irr-recording-profile.XXXXXX")"
  trap 'rm -rf "$tmp"' EXIT
  staged="$tmp/staged"
  dst="$tmp/recording"
  mkdir -p "$staged" "$dst"
  printf '%s\n' '{"seq":1,"ts":"2026-09-04T00:00:00Z"}' > "$staged/events.jsonl"
  printf '{"entrypoint":"%s"}\n' "$transcript_entrypoint" > "$staged/transcript.jsonl"
  if [[ "$profile" == "desktop-local" ]]; then
    for evidence_file in desktop-registry.json desktop-environment.json hooks.jsonl process.json irrlicht-session.json; do
      printf 'raw %s\n' "$evidence_file" > "$staged/$evidence_file"
    done
  fi

  # Export because the extracted production blocks read these values through
  # eval. The export also makes that indirect use visible to shellcheck.
  export AGENT="claudecode"
  export STAGED_DIR="$staged"
  export EXECUTION_PROFILE="$profile"
  export DESKTOP_APP_VERSION="$desktop_version"
  # This is the actual entrypoint reader from promote-recording.sh.
  eval "$identity_block"

  DAEMON_VER="0.6.2+abc1234"
  AGENT_VER="2.1.143"
  export RECIPE_HASH="recipe-sha"
  export NEW_STARTED_AT="2026-09-04T00:00:00Z"
  export GIT_HEAD_START="abc1234"
  export GIT_HEAD_END="abc1234"
  # This is the actual candidate writer from promote-recording.sh.
  eval "$population_block"
  populate_recording "$dst"

  if [[ "$(jq -r '.execution_profile' "$dst/manifest.json")" != "$profile" ]]; then
    exit 10
  fi
  if [[ "$(jq -r '.entrypoint' "$dst/manifest.json")" != "$transcript_entrypoint" ]]; then
    exit 11
  fi
  if [[ "$(jq -r '.daemon_version' "$dst/manifest.json")" != "$DAEMON_VER" ||
        "$(jq -r '.agent_cli_version' "$dst/manifest.json")" != "$AGENT_VER" ]]; then
    exit 12
  fi
  if [[ "$profile" == "desktop-local" ]]; then
    [[ "$(jq -r '.desktop_app_version' "$dst/manifest.json")" == "$desktop_version" ]] || exit 13
    for evidence_file in desktop-registry.json desktop-environment.json hooks.jsonl process.json irrlicht-session.json; do
      cmp -s "$staged/$evidence_file" "$dst/$evidence_file" || exit 15
    done
  elif jq -e 'has("desktop_app_version")' "$dst/manifest.json" >/dev/null; then
    exit 14
  fi
)

if check_manifest cli-local cli ""; then
  pass "CLI manifest keeps entrypoint and version identity"
else
  fail "CLI manifest keeps entrypoint and version identity" "writer output did not match the staged transcript"
fi

if check_manifest desktop-local sdk-cli 1.0.10; then
  pass "Desktop manifest keeps sdk-cli, app version, and raw evidence"
else
  fail "Desktop manifest keeps sdk-cli, app version, and raw evidence" "writer output did not match the staged evidence"
fi

echo ""
if [[ "$fails" -eq 0 ]]; then
  echo "recording-profile-manifest_test: ALL PASS"
else
  echo "recording-profile-manifest_test: $fails FAILED" >&2
  exit 1
fi
