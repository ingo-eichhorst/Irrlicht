#!/usr/bin/env bash
# promote-recording.sh — promote a staged recording into
# replaydata/agents/<agent>/<subtree>/<folder>/recordings/<name>/.
#
# There is no "latest" at the cell root: EVERY recording (newest included)
# lives under recordings/<name>/, holding all of its own data (events,
# transcript, manifest, and — added later by refresh-golden.sh — the golden).
# Re-recording just adds a new recordings/ folder; older ones stay so the
# viewer can show drift. expected.jsonl (the spec) and metadata.json (the cell
# descriptor) stay at the cell root.
#
# Layout produced:
#   replaydata/agents/<agent>/scenarios/<folder>/
#   ├── expected.jsonl              # spec — preserved at the root
#   ├── metadata.json               # cell descriptor — artifacts repointed here
#   └── recordings/
#       └── 2026-05-15-09-40-23_irrlichd-2.1.142/   # this promotion (newest)
#       │   ├── events.jsonl
#       │   ├── transcript.jsonl
#       │   └── manifest.json
#       └── 2026-05-14-…/           # earlier recordings, untouched
#
# Recording folder name: <first-event-ts hyphenated>_irrlichd-<daemon-ver>,
# which sorts newest-first by name (the viewer's ordering).
#
# Usage:
#   promote-recording.sh <staging-dir> <agent> <scenario-folder>
#
# <staging-dir> is the output of run-cell.sh
#   (.build/refresh/<agent>/<folder>-<TS>/).

set -euo pipefail

if [[ $# -ne 3 ]]; then
  echo "usage: promote-recording.sh <staging-dir> <agent> <scenario-folder>" >&2
  exit 2
fi

STAGING="$1"
AGENT="$2"
SCENARIO="$3"

REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || true)"
[[ -n "$REPO_ROOT" ]] || { echo "not in a git repo" >&2; exit 1; }

# shellcheck source=onboarding-factory/scripts/lib/shard-lib.sh
source "$REPO_ROOT/tools/onboarding-factory/scripts/lib/shard-lib.sh"

STAGED_DIR="$STAGING/replaydata/agents/$AGENT/scenarios/$SCENARIO"
TARGET_DIR="$REPO_ROOT/replaydata/agents/$AGENT/scenarios/$SCENARIO"
RECORDINGS_DIR="$TARGET_DIR/recordings"

if [[ ! -f "$STAGED_DIR/events.jsonl" ]]; then
  echo "promote: no staged events.jsonl at $STAGED_DIR" >&2
  exit 1
fi

# Daemon + agent CLI versions + recipe hash for the manifest.
DAEMON_VER="unknown"
for irrlichd_bin in "$REPO_ROOT/.build/refresh/bin/irrlichd" "$REPO_ROOT/.build/irrlichd" "$REPO_ROOT/core/bin/irrlichd"; do
  if [[ -x "$irrlichd_bin" ]]; then
    DAEMON_VER="$("$irrlichd_bin" --version 2>&1 | head -n1 | awk '{print $NF}' || echo unknown)"
    break
  fi
done
case "$AGENT" in
  claudecode) CLI_BIN="claude"; VER_FIELD=1 ;;
  codex)      CLI_BIN="codex";  VER_FIELD=2 ;;
  pi)         CLI_BIN="pi";     VER_FIELD=1 ;;
  aider)      CLI_BIN="aider";  VER_FIELD=2 ;;
  *)          CLI_BIN=""; VER_FIELD=1 ;;
esac
AGENT_VER="unknown"
if [[ -n "$CLI_BIN" ]] && command -v "$CLI_BIN" >/dev/null 2>&1; then
  AGENT_VER="$("$CLI_BIN" --version 2>&1 | awk -v f="$VER_FIELD" '{print $f}' | head -n1)"
fi
# recipe_hash pins the recorded recipe so the viewer can flag drift. $SCENARIO is
# the on-disk recording FOLDER, which for variant-folder cells is NOT the shard
# coverage_id — resolve it first so the hash isn't silently blank.
RECIPE_HASH=""
RECIPE_COV="$(shard_coverage_for_dir "$SCENARIO" "$AGENT")"
RECIPE_BLOB="$(shard_recipe "$RECIPE_COV" "$AGENT")"
if [[ -n "$RECIPE_BLOB" ]]; then
  RECIPE_HASH="$(printf '%s' "$RECIPE_BLOB" | shasum -a 256 | awk '{print $1}')"
fi

# 1. Name the new recording from its first-event ts (sorts newest-first by name).
NEW_TS="$(jq -r 'select(.ts) | .ts' "$STAGED_DIR/events.jsonl" 2>/dev/null | head -n1 || true)"
[[ -n "$NEW_TS" ]] || NEW_TS="$(date -u +%Y-%m-%dT%H-%M-%SZ)"
# ISO ts → filesystem-friendly stem: hyphenate, drop subseconds + zone.
STEM="$(echo "$NEW_TS" | sed -E 's/[T :]/-/g; s/\.[0-9]+//; s/[-+][0-9]{2}-?[0-9]{2}$//; s/Z$//')"
REC_NAME="${STEM}_irrlichd-${DAEMON_VER}"
REC_DIR="$RECORDINGS_DIR/$REC_NAME"
# Collision guard (two promotions within one second, or a re-record of the same
# captured ts): never clobber an existing recording.
if [[ -e "$REC_DIR" ]]; then
  n=2; while [[ -e "${REC_DIR}-$n" ]]; do n=$((n+1)); done
  REC_NAME="${REC_NAME}-$n"; REC_DIR="$RECORDINGS_DIR/$REC_NAME"
fi
mkdir -p "$REC_DIR"

# 2. Copy the staged recording into the new folder. transcript.md covers
#    markdown-transcript adapters (aider); transcript.json is the metadata
#    sidecar of sidecar-reading adapters (kiro-cli, #599) which replay stages
#    next to its scratch copy; -f guards no-op otherwise.
for f in events.jsonl transcript.jsonl transcript.md transcript.json; do
  if [[ -f "$STAGED_DIR/$f" ]]; then
    cp "$STAGED_DIR/$f" "$REC_DIR/$f"
  fi
done
echo "wrote recording $REC_DIR" >&2

# 3. Validate this recording against the cell's expected.jsonl, capturing the
#    pass rate for the manifest.
NEW_PASS_RATE=""
if [[ -f "$TARGET_DIR/expected.jsonl" ]]; then
  if VAL_OUT="$(cd "$REPO_ROOT" && go run ./tools/onboarding-factory/cmd/expected-validate "$TARGET_DIR" "$REC_NAME" 2>/dev/null)"; then
    NEW_PASS_RATE="$(echo "$VAL_OUT" | jq -r '.summary' 2>/dev/null || echo "")"
  else
    NEW_PASS_RATE="$(echo "$VAL_OUT" | jq -r '.summary' 2>/dev/null || echo "validate-failed")"
  fi
fi
NEW_STARTED_AT="$NEW_TS"

# 4. Write the recording's manifest.json.
jq -n \
  --arg promoted_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --arg daemon_version "$DAEMON_VER" \
  --arg agent_cli_version "$AGENT_VER" \
  --arg recipe_hash "$RECIPE_HASH" \
  --arg expected_pass_rate "$NEW_PASS_RATE" \
  --arg recording_started_at "$NEW_STARTED_AT" \
  '{
    promoted_at: $promoted_at,
    daemon_version: $daemon_version,
    agent_cli_version: $agent_cli_version,
    recipe_hash: $recipe_hash,
    expected_pass_rate: $expected_pass_rate,
    recording_started_at: $recording_started_at
  }' > "$REC_DIR/manifest.json"
echo "wrote $REC_DIR/manifest.json ($NEW_PASS_RATE)" >&2

# 5. metadata.json is NOT touched. The on-disk recordings/<name>/ tree is the
#    single source of truth: whether a cell is recorded, which recording is
#    newest, and where each file lives are all resolved from disk (the Go side
#    via validate.NewestRecordingDir / RecordingComplete). There is no artifacts
#    cache to repoint — that is what kept drifting from disk. The recording dir
#    written above (events/transcript/manifest, + the golden added next by
#    refresh-golden.sh) IS the record.

# 6. The promote does not auto-pass: if the new recording violates the spec,
#    exit non-zero so the maintainer reviews. The recording is already saved.
if [[ -f "$TARGET_DIR/expected.jsonl" ]]; then
  echo "validating new recording against expected.jsonl..." >&2
  if ! (cd "$REPO_ROOT" && go run ./tools/onboarding-factory/cmd/expected-validate "$TARGET_DIR" "$REC_NAME" >/dev/null 2>&1); then
    echo "WARNING: new recording fails expected.jsonl validation" >&2
    echo "         the recording is in place but the validator is unhappy" >&2
    echo "         either the recipe needs tightening (likely) or the daemon drifted from spec (file an issue)" >&2
    echo "         run: go run ./tools/onboarding-factory/cmd/expected-validate $TARGET_DIR $REC_NAME  for the report" >&2
    exit 3
  fi
fi

echo "promoted $REC_DIR" >&2
