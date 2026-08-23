#!/usr/bin/env bash
# precheck.sh — fail-fast correctness bundle for the onboarding factory's
# record path (of record run → run-cell.sh).
#
# Every check prints a specific failing-check name on stderr and exits
# nonzero so the skill can surface the exact reason. These checks are for
# correctness (port-clash, fixture-tree cleanliness, CLI compat) — they
# assume the agent CLI itself is already authenticated/subscribed by the
# user; auth failures surface through the CLI's own stderr.
#
# Usage:
#   precheck.sh <adapter>
#
#   adapter: claudecode | codex | pi (the adapter whose CLI version is checked
#            against min_versions in replaydata/scenarios/_meta.json). A second
#            positional is accepted-but-ignored for back-compat (#511 retired
#            the old <scenarios-json> arg).

set -euo pipefail

if [[ $# -lt 1 ]]; then
  echo "usage: precheck.sh <adapter>" >&2
  exit 2
fi

ADAPTER="$1"
# $2 (a scenarios-json path) is accepted but ignored for back-compat: since
# #511 the pinned min CLI versions live in replaydata/scenarios/_meta.json.

fail() {
  echo "precheck: $*" >&2
  exit 1
}

# 1. Adapter is supported by a driver.
case "$ADAPTER" in
  claudecode|codex|pi|aider|opencode|kiro-cli|gemini-cli|antigravity|mistral-vibe|copilot|hermes) ;;
  *)
    fail "unknown adapter: $ADAPTER"
    ;;
esac

# 2. Daemon expectation flips with $ATTACH:
#    - Default (isolated mode): the isolated daemon we launch binds port
#      7837, and so does the user's production irrlichd — racing them
#      would route hooks to the wrong process. Refuse if one is running.
#    - $ATTACH=1: caller wants run-cell to use the running daemon
#      instead of spawning its own. The dashboard stays connected for the
#      whole recording. Require that one IS running.
if [[ "${ATTACH:-0}" == "1" ]]; then
  if ! pgrep -x irrlichd >/dev/null 2>&1; then
    fail "ATTACH=1 but no irrlichd is running; start one with --record"
  fi
elif [[ -n "${IRRLICHT_ONBOARD_HOME:-}" ]]; then
  # Coexist mode: the recording daemon gets its OWN IRRLICHT_HOME (socket
  # + state) and an alternate bind port, so a running production irrlichd
  # is fine — they don't share a socket or port. We only require that OUR
  # target port is free.
  #
  # Hooks reaching $ONBOARD_BIND rather than production is NOT one
  # adapter-neutral guarantee — it is two different mechanisms, correct for
  # opposite reasons (#1754). URL delivery (claudecode, codex, copilot) bakes
  # the address INTO the installed entry at install time, so it always
  # reaches this daemon (#1178). Beacon delivery — every adapter importing
  # core/pkg/hookbeacon; as of this writing antigravity, gemini-cli, hermes,
  # kiro-cli, opencode, pi and mistral-vibe, but the LIST here is not the
  # source of truth and will drift — `righome.BeaconAdapters` (derived from
  # the import graph) and TestEveryBeaconAdapterDriverPassesTheDaemonAddress
  # (tools/onboarding-factory/internal/righome) are — writes NO address into
  # the entry at all; `irrlichd hook-post` resolves the daemon from its OWN
  # process environment at FIRE time, and that process is a child of the
  # agent CLI run-cell.sh launches under tmux. That only
  # reaches $ONBOARD_BIND because run-cell.sh explicitly exports
  # IRRLICHT_BIND_ADDR and forwards it into the tmux pane
  # (TestEveryBeaconAdapterDriverPassesTheDaemonAddress in
  # tools/onboarding-factory/internal/righome enforces it) — a caller that
  # drives the CLI some other way inherits none of that for free, and a
  # hook-free "healthy" recording is what #1735 measured when it didn't.
  ONBOARD_BIND="${IRRLICHT_ONBOARD_BIND_ADDR:-127.0.0.1:7838}"
  ONBOARD_PORT="${ONBOARD_BIND##*:}"
  if [[ ! "$ONBOARD_PORT" =~ ^[0-9]+$ ]]; then
    fail "IRRLICHT_ONBOARD_BIND_ADDR must be host:port with a numeric port (got '$ONBOARD_BIND')"
  fi
  if [[ "$ONBOARD_PORT" == "7837" ]]; then
    fail "coexist mode (IRRLICHT_ONBOARD_HOME set) needs a non-7837 IRRLICHT_ONBOARD_BIND_ADDR so it doesn't clash with production"
  fi
  if lsof -nP -iTCP:"$ONBOARD_PORT" -sTCP:LISTEN >/dev/null 2>&1; then
    fail "coexist port $ONBOARD_PORT is already in use; pick another IRRLICHT_ONBOARD_BIND_ADDR"
  fi
else
  if pgrep -x irrlichd >/dev/null 2>&1; then
    fail "another irrlichd is running (pgrep -x irrlichd); stop it first, rerun with --attach, or set IRRLICHT_ONBOARD_HOME + IRRLICHT_ONBOARD_BIND_ADDR to record on an isolated port"
  fi
fi

# 3. Clean working tree under replaydata/agents/. A dirty tree means the
#    maintainer already has staged fixture changes; we refuse to layer
#    another round on top.
REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || true)"
if [[ -z "$REPO_ROOT" ]]; then
  fail "not in a git repository"
fi
if [[ -n "$(cd "$REPO_ROOT" && git status --porcelain replaydata/agents/ 2>/dev/null)" ]]; then
  fail "replaydata/agents/ has uncommitted changes; commit or stash first"
fi

# 4. Adapter CLI present + version check against meta.min_versions in scenarios.json.
if ! command -v jq >/dev/null 2>&1; then
  fail "jq is required (brew install jq)"
fi
CATALOG_JSON="$REPO_ROOT/replaydata/agents/scenarios.json"
if [[ ! -f "$CATALOG_JSON" ]]; then
  fail "scenarios.json not found at $CATALOG_JSON"
fi
MIN_VERSION="$(jq -r --arg a "$ADAPTER" '.meta.min_versions[$a] // empty' "$CATALOG_JSON")"

# Version-string layout per adapter: <bin>:<awk-field-of-version-token>.
# claude --version → "X.Y.Z (...)"; codex --version → "codex-cli X.Y.Z";
# pi --version → "X.Y.Z"; aider --version → "aider X.Y.Z";
# kiro-cli --version → "kiro-cli X.Y.Z"; gemini --version → "X.Y.Z";
# agy --version → "X.Y.Z"; vibe --version → "vibe X.Y.Z" (verified against
# vibe 2.19.0 on this machine — the version is the 2nd field);
# copilot --version → "GitHub Copilot CLI 1.0.77." (verified against 1.0.77 —
# the version is the 4th field, and it carries a trailing period plus a
# second "Run 'copilot update'..." line, so the caller takes the head line).
case "$ADAPTER" in
  claudecode)  CLI_BIN="claude";   VER_FIELD=1 ;;
  codex)       CLI_BIN="codex";    VER_FIELD=2 ;;
  pi)          CLI_BIN="pi";       VER_FIELD=1 ;;
  aider)       CLI_BIN="aider";    VER_FIELD=2 ;;
  opencode)    CLI_BIN="opencode"; VER_FIELD=1 ;;
  kiro-cli)    CLI_BIN="kiro-cli"; VER_FIELD=2 ;;
  gemini-cli)  CLI_BIN="gemini";   VER_FIELD=1 ;;
  antigravity) CLI_BIN="agy";      VER_FIELD=1 ;;
  mistral-vibe) CLI_BIN="vibe";    VER_FIELD=2 ;;
  copilot)     CLI_BIN="copilot";  VER_FIELD=4 ;;
  hermes)      CLI_BIN="hermes";  VER_FIELD=3 ;;
  *) fail "unknown adapter: $ADAPTER" ;;
esac

command -v "$CLI_BIN" >/dev/null 2>&1 || fail "$CLI_BIN CLI not on PATH"
# Merge stderr — `pi --version` writes to stderr, others to stdout.
# Trailing punctuation is stripped because `copilot --version` prints
# "GitHub Copilot CLI 1.0.77." — with a full stop that would otherwise ride
# into the sort -V comparison below. No other adapter's version field ends in
# punctuation, so this is a no-op for them.
CLI_VER="$("$CLI_BIN" --version 2>&1 | awk -v f="$VER_FIELD" '{print $f}' | head -n1 | sed 's/[.,]$//')"
# Strip a leading "v" (hermes prints "Hermes Agent v0.19.0 ..."). Without
# this the floor check below silently passes ANY version: `sort -V` orders
# a bare "0.19.0" ahead of a "v"-prefixed string whatever the numbers say,
# so LOWEST always equals MIN_VERSION and the comparison proves nothing.
# A no-op for the adapters whose --version has no prefix.
CLI_VER="${CLI_VER#v}"
[[ -n "$CLI_VER" ]] || fail "could not parse '$CLI_BIN --version' output"

if [[ -n "$MIN_VERSION" ]]; then
  LOWEST="$(printf '%s\n%s\n' "$MIN_VERSION" "$CLI_VER" | sort -V | head -n1)"
  [[ "$LOWEST" == "$MIN_VERSION" ]] || fail "$ADAPTER $CLI_VER is below pinned minimum $MIN_VERSION"
fi

# 5. Build irrlichd + replay from the current worktree so recordings
#    reflect code under review, and so run-cell.sh can invoke replay
#    directly without paying a `go run` recompile per cell. The
#    -ldflags injection mirrors tools/build-dev.sh so the resulting
#    binary's --version output (and the daemon_version captured into
#    manifest.json by promote-recording.sh) carries the git sha +
#    .dirty flag instead of the bare "dev" string.
BIN_DIR="$REPO_ROOT/.build/refresh/bin"
mkdir -p "$BIN_DIR"
VERSION_STR="$("$REPO_ROOT/tools/version.sh" 2>/dev/null || echo dev)"
for bin in irrlichd replay; do
  # irrlichd lives in core; replay moved into the factory module (#523). go.work
  # resolves both from the repo root.
  case "$bin" in
    irrlichd) src="./core/cmd/irrlichd" ;;
    replay)   src="./tools/onboarding-factory/cmd/replay" ;;
    *) fail "unknown build target: $bin" ;;
  esac
  if ! (cd "$REPO_ROOT" && go build -ldflags "-X main.Version=$VERSION_STR" -o "$BIN_DIR/$bin" "$src") >/dev/null 2>&1; then
    fail "failed to build $bin from $src"
  fi
done

echo "precheck: OK (adapter=$ADAPTER, $ADAPTER=${CLI_VER:-n/a}, bin=$BIN_DIR)"

# Machine-readable copy for callers (#1333 / B3). The version above was already
# parsed correctly for all ten adapters, but it lived only in that human-readable
# line — which run-cell.sh does not capture — so promote-recording.sh re-derived
# it from a shorter table and stamped every copilot / gemini-cli / antigravity /
# mistral-vibe manifest `agent_cli_version: "unknown"`. That is the field a
# release sweep keys on, and the copilot run alone spanned two CLI builds
# (1.0.77 -> 1.0.78 mid-sweep), which is exactly when it matters.
if [[ -n "${PRECHECK_JSON_OUT:-}" ]]; then
  jq -nc \
    --arg adapter "$ADAPTER" \
    --arg cli_bin "$CLI_BIN" \
    --arg cli_version "${CLI_VER:-}" \
    --arg min_version "${MIN_VERSION:-}" \
    --arg daemon_version "$VERSION_STR" \
    '{adapter: $adapter, cli_bin: $cli_bin, cli_version: $cli_version,
      min_version: $min_version, daemon_version: $daemon_version}' \
    > "$PRECHECK_JSON_OUT" 2>/dev/null || true
fi
