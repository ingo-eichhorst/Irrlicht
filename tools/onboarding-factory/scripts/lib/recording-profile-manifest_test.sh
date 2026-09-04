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

if check_manifest desktop-local claude-desktop 1.0.10; then
  pass "Desktop manifest keeps claude-desktop, app version, and raw evidence"
else
  fail "Desktop manifest keeps claude-desktop, app version, and raw evidence" "writer output did not match the staged evidence"
fi

echo "== a Desktop run stamps only a version something measured =="
# precheck.sh cannot measure the Claude Code build inside Claude.app; the
# Desktop helper does, and writes $STAGING/desktop.versions.json. precheck used
# to park the sentinel "verified-by-desktop-helper" in precheck.json's
# cli_version, and this chain picked it up whenever desktop.versions.json was
# missing — stamping the committed manifest with a version nothing had read.
version_chain_block="$(extract_block recording_version_chain)"
if [[ $? -ne 0 || -z "$version_chain_block" ]]; then
  fail "version chain extracted" "marker count changed or the block was empty"
else
  pass "version chain extracted"
fi

# check_version <helper-version|-> <precheck-cli-version|-> prints AGENT_VER.
check_version() (
  local helper="$1" precheck="$2" tmp
  tmp="$(mktemp -d "${TMPDIR:-/tmp}/irr-version-chain.XXXXXX")"
  trap 'rm -rf "$tmp"' EXIT
  [[ "$helper" == "-" ]] ||
    printf '{"claude_code":"%s"}\n' "$helper" > "$tmp/desktop.versions.json"
  [[ "$precheck" == "-" ]] ||
    printf '{"cli_version":"%s"}\n' "$precheck" > "$tmp/precheck.json"
  STAGING="$tmp" AGENT="claudecode" EXECUTION_PROFILE="desktop-local" \
    bash -c 'set -uo pipefail; '"$version_chain_block"'; printf "%s" "$AGENT_VER"'
)

assert_version() {
  local label="$1" want="$2" got="$3"
  [[ "$want" == "$got" ]] && pass "$label" || fail "$label" "expected [$want] got [$got]"
}

assert_version "the helper's measured version wins" "2.1.258" \
  "$(check_version 2.1.258 -)"
assert_version "no helper version leaves the field empty, not a sentinel" "" \
  "$(check_version - -)"
if grep -Fq 'verified-by-desktop-helper' "$ROOT/tools/onboarding-factory/scripts/precheck.sh"; then
  got="sentinel"
else
  got="empty"
fi
assert_version "precheck writes no Desktop version sentinel" "empty" "$got"

echo "== the promoted profile must agree with what the run recorded =="
# The flag alone used to decide whether every Desktop gate ran, and its default
# is cli-local. Extract the real cross-check and drive all four combinations.
crosscheck_block="$(extract_block recording_profile_crosscheck)"
if [[ $? -ne 0 || -z "$crosscheck_block" ]]; then
  fail "profile cross-check extracted" "marker count changed or the block was empty"
else
  pass "profile cross-check extracted"
fi

# check_profile <flag-profile> <manifest-profile|-> <desktop-evidence:yes|no>
# prints the exit status of the real block.
check_profile() (
  local flag="$1" manifest="$2" evidence="$3" tmp
  tmp="$(mktemp -d "${TMPDIR:-/tmp}/irr-profile-xcheck.XXXXXX")"
  trap 'rm -rf "$tmp"' EXIT
  mkdir -p "$tmp/staged"
  [[ "$manifest" == "-" ]] ||
    printf '{"execution_profile":"%s"}\n' "$manifest" > "$tmp/run-manifest.json"
  if [[ "$evidence" == "yes" ]]; then
    printf 'raw\n' > "$tmp/staged/desktop-registry.json"
  fi
  STAGING="$tmp" STAGED_DIR="$tmp/staged" EXECUTION_PROFILE="$flag" \
    bash -c 'set -uo pipefail; '"$crosscheck_block"'; exit 0' >/dev/null 2>&1
  echo $?
)

assert_profile() {
  local label="$1" want="$2" got="$3"
  [[ "$want" == "$got" ]] && pass "$label" || fail "$label" "expected exit $want, got $got"
}

assert_profile "a matching cli-local run promotes" 0 \
  "$(check_profile cli-local cli-local no)"
assert_profile "a matching desktop-local run promotes" 0 \
  "$(check_profile desktop-local desktop-local yes)"
assert_profile "a Desktop run promoted as cli-local is refused by the manifest" 2 \
  "$(check_profile cli-local desktop-local yes)"
assert_profile "a Desktop tree with no manifest is still refused by its evidence" 2 \
  "$(check_profile cli-local - yes)"
assert_profile "a cli run promoted as desktop-local is refused by the manifest" 2 \
  "$(check_profile desktop-local cli-local no)"
assert_profile "an old staging tree with no manifest and no evidence still promotes" 0 \
  "$(check_profile cli-local - no)"

echo ""
if [[ "$fails" -eq 0 ]]; then
  echo "recording-profile-manifest_test: ALL PASS"
else
  echo "recording-profile-manifest_test: $fails FAILED" >&2
  exit 1
fi
