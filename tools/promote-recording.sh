#!/usr/bin/env bash
# promote-recording.sh — promote a staged recording to replaydata/,
# archiving the previous version into recordings/<ts>_<daemon-ver>/.
#
# Why archive: re-recordings shouldn't silently erase the previous
# observation. Drift in the daemon's behavior is the signal we want
# to preserve — archived recordings + manifest.json let the viewer
# show "this is what daemon version X observed; this is what version
# Y observes now". expected.jsonl stays the constant benchmark across
# all of them.
#
# Layout produced:
#   replaydata/agents/<agent>/scenarios/<scenario>/
#   ├── expected.jsonl              # spec — preserved
#   ├── events.jsonl                # NEW (from staging)
#   ├── transcript.jsonl            # NEW (from staging)
#   ├── manifest.json               # NEW (top-level recording metadata)
#   └── recordings/
#       └── 2026-05-15T09-40_irrlichd-2.1.142/
#           ├── events.jsonl        # PREVIOUS latest, moved here
#           ├── transcript.jsonl
#           └── manifest.json
#
# Manifest fields:
#   promoted_at         — UTC of this promotion
#   daemon_version      — from irrlichd --version
#   agent_cli_version   — from <claude|codex|pi|aider> --version
#   recipe_hash         — sha256 of by_adapter.<agent> in scenarios.json
#   expected_pass_rate  — frozen validator summary at archive time
#   recording_started_at — first event's ts in the archived recording
#
# Usage:
#   promote-recording.sh <staging-dir> <agent> <scenario>
#
# <staging-dir> is the output of run-cell.sh
#   (.build/refresh/<agent>/<scenario>-<TS>/).

set -euo pipefail

if [[ $# -ne 3 ]]; then
  echo "usage: promote-recording.sh <staging-dir> <agent> <scenario>" >&2
  exit 2
fi

STAGING="$1"
AGENT="$2"
SCENARIO="$3"

REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || true)"
[[ -n "$REPO_ROOT" ]] || { echo "not in a git repo" >&2; exit 1; }

STAGED_DIR="$STAGING/replaydata/agents/$AGENT/scenarios/$SCENARIO"
TARGET_DIR="$REPO_ROOT/replaydata/agents/$AGENT/scenarios/$SCENARIO"
RECORDINGS_DIR="$TARGET_DIR/recordings"

if [[ ! -f "$STAGED_DIR/events.jsonl" ]]; then
  echo "promote: no staged events.jsonl at $STAGED_DIR" >&2
  exit 1
fi

# Detect daemon + agent CLI versions + recipe hash ONCE — both the
# archive manifest (step 1) and the new top-level manifest (step 3)
# need them, so computing here avoids duplication and keeps the two
# manifests internally consistent within one promote.
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
RECIPE_HASH=""
SCENARIOS_JSON="$REPO_ROOT/.claude/skills/ir:onboard-agent/scenarios.json"
if [[ -f "$SCENARIOS_JSON" ]]; then
  RECIPE_BLOB="$(jq -c --arg s "$SCENARIO" --arg a "$AGENT" \
    '.scenarios[] | select(.name == $s) | .by_adapter[$a]' "$SCENARIOS_JSON" 2>/dev/null || true)"
  if [[ -n "$RECIPE_BLOB" && "$RECIPE_BLOB" != "null" ]]; then
    RECIPE_HASH="$(printf '%s' "$RECIPE_BLOB" | shasum -a 256 | awk '{print $1}')"
  fi
fi

# 1. Archive the current top-level recording if one exists.
if [[ -f "$TARGET_DIR/events.jsonl" ]]; then
  # Read the previous recording's first-event ts to name the archive.
  PREV_TS="$(jq -r 'select(.ts) | .ts' "$TARGET_DIR/events.jsonl" | head -n1)"
  if [[ -z "$PREV_TS" ]]; then
    PREV_TS="$(date -u +%Y-%m-%dT%H-%M-%SZ)"
  fi
  # ISO timestamps contain colons which aren't filesystem-friendly on
  # all platforms. Replace them with hyphens; strip subsecond + zone.
  ARCHIVE_TS="$(echo "$PREV_TS" | sed -E 's/[T :]/-/g; s/\.[0-9]+//; s/[-+][0-9]{2}-?[0-9]{2}$//; s/Z$//')"

  ARCHIVE_NAME="${ARCHIVE_TS}_irrlichd-${DAEMON_VER}"
  ARCHIVE_DIR="$RECORDINGS_DIR/$ARCHIVE_NAME"
  mkdir -p "$ARCHIVE_DIR"
  echo "archiving previous recording to $ARCHIVE_DIR" >&2

  # Move the current trio into the archive. transcript.md covers
  # markdown-transcript adapters (aider); the -f guard no-ops for the
  # rest, which use transcript.jsonl.
  for f in events.jsonl transcript.jsonl transcript.md; do
    if [[ -f "$TARGET_DIR/$f" ]]; then
      mv "$TARGET_DIR/$f" "$ARCHIVE_DIR/$f"
    fi
  done

  # Expected validation against the archived events.jsonl. Frozen
  # here at archive time so the viewer can display "this is what the
  # validator said about this run when it was promoted" without
  # re-running the validator against arbitrary historical events.
  EXPECTED_PASS_RATE=""
  if [[ -f "$TARGET_DIR/expected.jsonl" ]]; then
    # Run the validator against the ARCHIVED dir (where events.jsonl
    # now lives) — needs the expected.jsonl alongside. Copy it
    # temporarily, validate, remove.
    cp "$TARGET_DIR/expected.jsonl" "$ARCHIVE_DIR/expected.jsonl"
    if VAL_OUT="$(cd "$REPO_ROOT" && go run ./tools/agent-onboarding/cmd/expected-validate "$ARCHIVE_DIR" 2>/dev/null)"; then
      EXPECTED_PASS_RATE="$(echo "$VAL_OUT" | jq -r '.summary' 2>/dev/null || echo "")"
    else
      EXPECTED_PASS_RATE="$(echo "$VAL_OUT" | jq -r '.summary' 2>/dev/null || echo "validate-failed")"
    fi
    rm -f "$ARCHIVE_DIR/expected.jsonl"
  fi

  jq -n \
    --arg promoted_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    --arg daemon_version "$DAEMON_VER" \
    --arg agent_cli_version "$AGENT_VER" \
    --arg recipe_hash "$RECIPE_HASH" \
    --arg expected_pass_rate "$EXPECTED_PASS_RATE" \
    --arg recording_started_at "$PREV_TS" \
    '{
      promoted_at: $promoted_at,
      daemon_version: $daemon_version,
      agent_cli_version: $agent_cli_version,
      recipe_hash: $recipe_hash,
      expected_pass_rate: $expected_pass_rate,
      recording_started_at: $recording_started_at
    }' > "$ARCHIVE_DIR/manifest.json"

  echo "wrote $ARCHIVE_DIR/manifest.json ($EXPECTED_PASS_RATE)" >&2
fi

# 2. Copy the staged recording into the top-level slot. transcript.md
# covers markdown-transcript adapters (aider); -f guard no-ops otherwise.
mkdir -p "$TARGET_DIR"
for f in events.jsonl transcript.jsonl transcript.md; do
  if [[ -f "$STAGED_DIR/$f" ]]; then
    cp "$STAGED_DIR/$f" "$TARGET_DIR/$f"
  fi
done

# 3. Write a top-level manifest.json describing the new latest. Same
#    schema as archive manifests so the viewer renders a uniform
#    metadata panel for Latest vs archives. The viewer falls back to a
#    synthesized manifest when this file is missing (e.g. recordings
#    promoted before this step existed), so dropping the file is safe.
NEW_PASS_RATE=""
if [[ -f "$TARGET_DIR/expected.jsonl" ]]; then
  if VAL_OUT="$(cd "$REPO_ROOT" && go run ./tools/agent-onboarding/cmd/expected-validate "$TARGET_DIR" 2>/dev/null)"; then
    NEW_PASS_RATE="$(echo "$VAL_OUT" | jq -r '.summary' 2>/dev/null || echo "")"
  else
    NEW_PASS_RATE="$(echo "$VAL_OUT" | jq -r '.summary' 2>/dev/null || echo "validate-failed")"
  fi
fi
NEW_STARTED_AT="$(head -n1 "$TARGET_DIR/events.jsonl" 2>/dev/null | jq -r '.ts' 2>/dev/null || echo "")"
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
  }' > "$TARGET_DIR/manifest.json"
echo "wrote $TARGET_DIR/manifest.json ($NEW_PASS_RATE)" >&2

# 4. Validate the new recording against expected.jsonl. The promote
#    step DOES NOT auto-pass — if the new recording violates the
#    spec, exit non-zero so the maintainer reviews before the
#    archive becomes the new latest. (The previous version is
#    already safely archived; rollback is `mv recordings/<latest>/* .`)
if [[ -f "$TARGET_DIR/expected.jsonl" ]]; then
  echo "validating new recording against expected.jsonl..." >&2
  if ! (cd "$REPO_ROOT" && go run ./tools/agent-onboarding/cmd/expected-validate "$TARGET_DIR" >/dev/null 2>&1); then
    echo "WARNING: new recording fails expected.jsonl validation" >&2
    echo "         the recording is in place but the validator is unhappy" >&2
    echo "         either the recipe needs tightening (likely) or the daemon drifted from spec (file an issue)" >&2
    echo "         run: go run ./tools/agent-onboarding/cmd/expected-validate $TARGET_DIR  for the report" >&2
    exit 3
  fi
fi

echo "promoted $TARGET_DIR" >&2
