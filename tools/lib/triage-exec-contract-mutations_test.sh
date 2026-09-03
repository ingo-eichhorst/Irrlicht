#!/usr/bin/env bash
# Prove that the triage-to-exec contract guard fails on representative drift.
set -uo pipefail

DIR=$(cd "$(dirname "$0")" && pwd)
REPO_ROOT=$(cd "$DIR/../.." && pwd)
# shellcheck disable=SC2034  # read by assert_mutation_is_red in mutation-assert.sh
MUTATE_SH=$REPO_ROOT/tools/mutate.sh
# shellcheck disable=SC2034  # read by assert_mutation_is_red in mutation-assert.sh
LOCK_TEST=tools/lib/triage-exec-contract_test.sh

# shellcheck source=tools/lib/mutation-assert.sh
. "$DIR/mutation-assert.sh"

if [[ -n "$(git -C "$REPO_ROOT" status --porcelain)" ]]; then
  echo 'triage-exec-contract-mutations: CANNOT RUN — worktree is dirty' >&2
  if [[ -n "${CI:-}" || -n "${MUTATION_FIXTURES_STRICT:-}" ]]; then exit 1; fi
  exit 0
fi

fails=0

assert_mutation_is_red \
  'guard catches Process removed from triage output' \
  '.claude/skills/ir:triage/SKILL.md' \
  $'**Process:** investigation <direct | 1 Explore | 1–2 Explore | 2–3 Explore> · review <low | medium | high> · simplify <inline | /simplify>' \
  $'**Execution:** investigation <direct | 1 Explore | 1–2 Explore | 2–3 Explore> · review <low | medium | high> · simplify <inline | /simplify>' \
  'triage assessment template missing required field: **Process:**'

assert_mutation_is_red \
  'guard catches Process removed from exec input' \
  '.claude/skills/ir:exec/SKILL.md' \
  $'- `**Process:**` with investigation, review, and simplify values;' \
  $'- `**Execution:**` with investigation, review, and simplify values;' \
  'exec validation missing required field: **Process:**'

assert_mutation_is_red \
  'guard catches a removed execution mode returning' \
  '.claude/skills/ir:exec/SKILL.md' \
  $'```text\n/ir:exec <N>\n```' \
  $'```text\n/ir:exec auto <N>\n```' \
  'exec documents unsupported mode: /ir:exec auto'

if [[ $fails -gt 0 ]]; then
  echo "triage-exec-contract-mutations: $fails FAILED"
  exit 1
fi
echo 'triage-exec-contract-mutations: ALL PASS'
