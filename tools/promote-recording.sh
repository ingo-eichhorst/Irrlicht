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
# Validate-then-write (#1333 / B2): a recording that fails its spec must never
# reach the tree.
# shellcheck source=onboarding-factory/scripts/lib/atomic-promote.sh
source "$REPO_ROOT/tools/onboarding-factory/scripts/lib/atomic-promote.sh"

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
# agent_cli_version (#1333 / B3). precheck.sh already parsed this correctly,
# seconds earlier, for all TEN adapters — it just stranded the value in a
# human-readable stdout line no caller captured, so every manifest recorded
# "unknown". It now stages precheck.json; prefer that.
# Two staging shapes: run-cell.sh drives one adapter and writes
# $STAGING/precheck.json; run-cell-multi.sh drives several into one staging tree
# and writes $STAGING/<agent>/precheck.json. Check the per-adapter path first so
# a multi run can't pick up a sibling adapter's version.
AGENT_VER="$(jq -r '.cli_version // empty' "$STAGING/$AGENT/precheck.json" 2>/dev/null || true)"
[[ -n "$AGENT_VER" ]] || AGENT_VER="$(jq -r '.cli_version // empty' "$STAGING/precheck.json" 2>/dev/null || true)"
if [[ -z "$AGENT_VER" ]]; then
  # Fallback for a promote that didn't come through run-cell.sh. This table was
  # SIX entries long while precheck's was ten, which is the other half of the
  # bug: copilot, gemini-cli, antigravity and mistral-vibe fell through to the
  # `*)` arm, so every one of their recordings was stamped "unknown" even when
  # the CLI was right there on PATH.
  #
  # Two tables that must agree is itself the hazard — adding an adapter to
  # precheck and forgetting this one silently reintroduces the bug, which is
  # exactly what happened when hermes landed (#1327) while this fix was in
  # flight. tools/onboarding-factory/scripts/lib/adapter-tables_test.sh now
  # fails when they diverge; keep it in step with precheck.sh's case block.
  #
  # That test is a HOLDING PATTERN, not the intended end state. The right home
  # for this data is replaydata/agents/scenarios.json's `.meta`, which already
  # carries exactly this shape of per-adapter map (`min_versions`,
  # `transcript_extensions`) and is already read by both scripts —
  # precheck.sh reads it inline, and this file sources shard-lib.sh, which
  # exposes meta_min_version()/meta_transcript_ext(). A
  # `.meta.cli_version_probe: {"copilot": {"bin": "copilot", "field": 4}, …}`
  # plus one accessor would delete both case blocks AND the test guarding them.
  # Deferred out of #1333 because that PR's subject is the recording guards, not
  # the catalog schema, and this block is now only a fallback (the primary path
  # reads precheck.json). Tracked for follow-up.
  case "$AGENT" in
    claudecode)   CLI_BIN="claude";   VER_FIELD=1 ;;
    codex)        CLI_BIN="codex";    VER_FIELD=2 ;;
    pi)           CLI_BIN="pi";       VER_FIELD=1 ;;
    aider)        CLI_BIN="aider";    VER_FIELD=2 ;;
    opencode)     CLI_BIN="opencode"; VER_FIELD=1 ;;
    kiro-cli)     CLI_BIN="kiro-cli"; VER_FIELD=2 ;;
    gemini-cli)   CLI_BIN="gemini";   VER_FIELD=1 ;;
    antigravity)  CLI_BIN="agy";      VER_FIELD=1 ;;
    mistral-vibe) CLI_BIN="vibe";     VER_FIELD=2 ;;
    copilot)      CLI_BIN="copilot";  VER_FIELD=4 ;;
    hermes)       CLI_BIN="hermes";   VER_FIELD=3 ;;
    *)            CLI_BIN=""; VER_FIELD=1 ;;
  esac
  AGENT_VER="unknown"
  if [[ -n "$CLI_BIN" ]] && command -v "$CLI_BIN" >/dev/null 2>&1; then
    # Same trailing-punctuation strip as precheck.sh: `copilot --version` prints
    # "GitHub Copilot CLI 1.0.77." and the period would ride into the manifest.
    AGENT_VER="$("$CLI_BIN" --version 2>&1 | awk -v f="$VER_FIELD" '{print $f}' | head -n1 | sed 's/[.,]$//')"
    # ...and the same leading-"v" strip: hermes prints "Hermes Agent v0.19.0".
    AGENT_VER="${AGENT_VER#v}"
    [[ -n "$AGENT_VER" ]] || AGENT_VER="unknown"
  fi
fi

# Repo provenance (#1333 / B7). Serialized recording is already the documented
# contract, but nothing enforced it: a concurrent session committing in the same
# worktree mid-recording leaves manifests whose daemon_version SHA belongs to
# another session's commits. Stamping the HEAD at run start and at driver exit
# makes the smear visible rather than silent. run-cell.sh records both; fall
# back to the current HEAD when promoting outside that path.
HEAD_NOW="$(git -C "$REPO_ROOT" rev-parse --short HEAD 2>/dev/null || echo unknown)"
GIT_HEAD_START="$(jq -r '.git_head_start // empty' "$STAGING/run-manifest.json" 2>/dev/null || true)"
GIT_HEAD_END="$(jq -r '.git_head_end // empty' "$STAGING/run-manifest.json" 2>/dev/null || true)"
[[ -n "$GIT_HEAD_START" ]] || GIT_HEAD_START="$HEAD_NOW"
[[ -n "$GIT_HEAD_END" ]]   || GIT_HEAD_END="$HEAD_NOW"
if [[ "$GIT_HEAD_START" != "$GIT_HEAD_END" ]]; then
  echo "WARNING: HEAD moved during this recording ($GIT_HEAD_START -> $GIT_HEAD_END)" >&2
  echo "         another session committed in this worktree while it recorded, so" >&2
  echo "         daemon_version may name a build that did not produce this data." >&2
  echo "         Serialized recording assumes exclusive use of the worktree." >&2
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
NEW_STARTED_AT="$NEW_TS"

# 2. Fill a CANDIDATE recording dir. atomic_promote hands us a scratch path; the
#    recording only becomes real if the validator accepts it (#1333 / B2).
#    transcript.md covers markdown-transcript adapters (aider); transcript.json
#    is the metadata sidecar of sidecar-reading adapters (kiro-cli, #599) which
#    replay stages next to its scratch copy; -f guards no-op otherwise.
populate_recording() {
  local dst="$1" f
  for f in events.jsonl transcript.jsonl transcript.md transcript.json; do
    if [[ -f "$STAGED_DIR/$f" ]]; then
      cp "$STAGED_DIR/$f" "$dst/$f"
    fi
  done
  # Antigravity usage store (#766): curate captured the sibling SQLite store
  # under store/conversations/; carry the whole subtree so replay can rebuild the
  # live layout and resolve tokens + the canonical model. RecordingComplete
  # checks only required-file presence, so this extra subtree keeps `of validate`
  # green.
  if [[ -d "$STAGED_DIR/store" ]]; then
    cp -R "$STAGED_DIR/store" "$dst/store"
  fi
  # manifest.json is written INSIDE the candidate so a rejected recording leaves
  # no manifest either. expected_pass_rate is filled in by the caller after the
  # validator has spoken — it cannot be known before the candidate exists, so it
  # is stamped in a second pass below.
  jq -n \
    --arg promoted_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    --arg daemon_version "$DAEMON_VER" \
    --arg agent_cli_version "$AGENT_VER" \
    --arg recipe_hash "$RECIPE_HASH" \
    --arg recording_started_at "$NEW_STARTED_AT" \
    --arg git_head_start "$GIT_HEAD_START" \
    --arg git_head_end "$GIT_HEAD_END" \
    '{
      promoted_at: $promoted_at,
      daemon_version: $daemon_version,
      agent_cli_version: $agent_cli_version,
      recipe_hash: $recipe_hash,
      expected_pass_rate: "",
      recording_started_at: $recording_started_at,
      git_head_start: $git_head_start,
      git_head_end: $git_head_end
    }' > "$dst/manifest.json"
}

# 3. Validate the candidate against the cell's expected.jsonl. Echoes the pass
#    rate; non-zero rejects. Run ONCE now, where it gates — the old flow ran
#    expected-validate twice, once for the manifest field and once for the gate.
validate_recording() {
  local cell_dir="$1" rec_name="$2" out
  if out="$(cd "$REPO_ROOT" && go run ./tools/onboarding-factory/cmd/expected-validate "$cell_dir" "$rec_name" 2>/dev/null)"; then
    echo "$out" | jq -r '.summary' 2>/dev/null || echo ""
    return 0
  fi
  echo "$out" | jq -r '.summary' 2>/dev/null || echo "validate-failed"
  return 1
}

echo "validating candidate recording against expected.jsonl..." >&2
# An assignment inside an AND-OR list is not errexit-fatal, and `$?` in the ||
# arm is the function's return code — so this captures rc 0/1/3 without turning
# errexit off for a span of script.
NEW_PASS_RATE="$(atomic_promote "$TARGET_DIR" "$REC_NAME" populate_recording validate_recording)" \
  && PROMOTE_RC=0 || PROMOTE_RC=$?

if [[ "$PROMOTE_RC" == "3" ]]; then
  echo "WARNING: new recording fails expected.jsonl validation ($NEW_PASS_RATE)" >&2
  echo "         NOTHING was written — the candidate was discarded, so there is no" >&2
  echo "         half-promoted directory to rm -rf before re-promoting." >&2
  echo "         either the recipe needs tightening (likely) or the daemon drifted from spec (file an issue)" >&2
  echo "         re-run with the staged capture to see the full report." >&2
  exit 3
fi
if [[ "$PROMOTE_RC" != "0" ]]; then
  echo "promote: failed to stage the recording into $REC_DIR" >&2
  exit 1
fi

# 4. Stamp the pass rate the validator reported. The manifest was written inside
#    the candidate (so a rejected recording leaves none); only the rate has to
#    wait for the verdict.
# The scratch file lives OUTSIDE the recording: on a jq failure `set -e` exits
# here with the recording already promoted, and a manifest.json.tmp inside
# recordings/<name>/ would be swept up by `record`'s next `git add <cell-dir>/`.
TMP_MANIFEST="$(mktemp -t irr-manifest.XXXXXX)"
if jq --arg r "$NEW_PASS_RATE" '.expected_pass_rate = $r' "$REC_DIR/manifest.json" > "$TMP_MANIFEST"; then
  mv "$TMP_MANIFEST" "$REC_DIR/manifest.json"
else
  rm -f "$TMP_MANIFEST"
  echo "WARNING: could not stamp expected_pass_rate into $REC_DIR/manifest.json" >&2
fi
echo "wrote recording $REC_DIR" >&2
echo "wrote $REC_DIR/manifest.json ($NEW_PASS_RATE)" >&2

# 5. metadata.json is NOT touched. The on-disk recordings/<name>/ tree is the
#    single source of truth: whether a cell is recorded, which recording is
#    newest, and where each file lives are all resolved from disk (the Go side
#    via validate.NewestRecordingDir / RecordingComplete). There is no artifacts
#    cache to repoint — that is what kept drifting from disk. The recording dir
#    written above (events/transcript/manifest, + the golden added next by
#    refresh-golden.sh) IS the record.

echo "promoted $REC_DIR" >&2
