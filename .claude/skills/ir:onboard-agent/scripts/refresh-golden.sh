#!/usr/bin/env bash
# refresh-golden.sh — regenerate the replay byte-identity golden(s) for ONE
# scenario's committed recording(s), leaving every other adapter/scenario
# golden untouched.
#
# Why this exists: TestFixtureReplayByteIdentity (core/cmd/replay) pins the
# replay JSON of every committed transcript to a sibling
# `transcript.jsonl.replay.json.golden`. `promote-recording.sh` writes the
# recording but NOT that golden, so a fresh recording leaves `go test
# ./core/...` red until the golden is generated. The implement stage calls
# this right after promote so the recording ships with its matching golden.
#
# Why scoped: the golden test has no per-fixture UPDATE flag —
# UPDATE_REPLAY_GOLDENS=1 rewrites EVERY golden in the tree, including
# pre-existing drift in unrelated adapters. We regenerate all, then discard
# every golden change that is NOT under this scenario. That keeps the
# implement stage's "leave no dirty tree" contract and never masks another
# cell's drift (which belongs to its own maintainer task / issue).
#
# Usage:
#   refresh-golden.sh <agent> <scenario>
#
# Idempotent: if the recording already matches its golden, it reports "no
# golden change" and exits 0 (nothing staged).

set -euo pipefail

AGENT="${1:?usage: refresh-golden.sh <agent> <scenario>}"
SCENARIO="${2:?usage: refresh-golden.sh <agent> <scenario>}"

REPO_ROOT="$(git rev-parse --show-toplevel)"
cd "$REPO_ROOT"

SCEN="replaydata/agents/${AGENT}/scenarios/${SCENARIO}"
if [[ ! -d "$SCEN" ]]; then
  echo "refresh-golden: no scenario dir $SCEN" >&2
  exit 1
fi

# Regenerate ALL goldens. The test has no per-fixture filter, and -count=1 is
# REQUIRED: a cached `go test` run does not execute the test body, so the
# UPDATE_REPLAY_GOLDENS side effect never fires and no goldens are written.
echo "refresh-golden: regenerating goldens (UPDATE_REPLAY_GOLDENS=1)..." >&2
if ! UPDATE_REPLAY_GOLDENS=1 go test ./core/cmd/replay/... -count=1 \
       -run TestFixtureReplayByteIdentity >/dev/null 2>&1; then
  echo "refresh-golden: golden regeneration failed (replay build/test error)" >&2
  exit 1
fi

# Revert modified tracked goldens that are NOT under this scenario.
while IFS= read -r f; do
  [[ -z "$f" ]] && continue
  git checkout -- "$f"
done < <(git diff --name-only -- '*.replay.json.golden' | grep -v "^${SCEN}/" || true)

# Remove newly-created (untracked) goldens that are NOT under this scenario.
while IFS= read -r f; do
  [[ -z "$f" ]] && continue
  rm -f "$f"
done < <(git ls-files --others --exclude-standard -- '*.replay.json.golden' \
           | grep -v "^${SCEN}/" || true)

if git status --porcelain -- "$SCEN" | grep -q '\.replay\.json\.golden$'; then
  echo "refresh-golden: refreshed golden(s) under ${SCEN}:" >&2
  git status --porcelain -- "$SCEN" | grep '\.replay\.json\.golden$' >&2
else
  echo "refresh-golden: no golden change (recording already matches golden)" >&2
fi
