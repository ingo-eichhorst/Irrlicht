#!/usr/bin/env bash
# discover-agent.sh — render the discovery preamble for a new agent.
#
# Used by skill.md when invoked as `/ir:onboard-agent --new <slug>`.
# Prints the closed-vocabulary preamble that gets prepended to each
# discovery subagent's task prompt. The skill itself does the actual
# Agent dispatch — this script is a pure formatter, no web access.
#
# Usage:
#   scripts/discover-agent.sh <slug> [agent|orchestrator]
#
# Default kind is `agent`.

set -euo pipefail

if [[ $# -lt 1 ]]; then
  echo "usage: $0 <slug> [agent|orchestrator]" >&2
  exit 2
fi

SLUG="$1"
KIND="${2:-agent}"

REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"

case "$KIND" in
  agent)        FEATURES_JSON="$REPO_ROOT/replaydata/agents/features.json" ;;
  orchestrator) FEATURES_JSON="$REPO_ROOT/replaydata/orchestrators/features.json" ;;
  *) echo "unknown kind: $KIND (want: agent | orchestrator)" >&2; exit 2 ;;
esac

[[ -f "$FEATURES_JSON" ]] || { echo "missing canonical features list: $FEATURES_JSON" >&2; exit 1; }

cat <<EOF
You are researching the coding $KIND "$SLUG" for the irrlicht onboarding skill.
You will report whether the $KIND supports each of the following capabilities,
based on documentation and community sources. Use only the IDs listed here.
If you observe a behavior that does not map to any listed ID, note it
separately under "candidate_new_features".

Closed vocabulary (id — title — description):
EOF

jq -r '.features[] | "  \(.id) — \(.title) — \(.description)"' "$FEATURES_JSON"
