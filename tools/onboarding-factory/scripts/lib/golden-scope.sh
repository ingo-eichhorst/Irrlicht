#!/usr/bin/env bash
# golden-scope.sh — decide which replay goldens refresh-golden.sh may undo.
#
# Sourced by refresh-golden.sh. Pure list arithmetic: no git, no filesystem, so
# it is unit-testable without a repo or a `go test` run.
#
# The problem it solves. The golden test has no per-fixture UPDATE flag —
# UPDATE_REPLAY_GOLDENS=1 rewrites EVERY golden in the tree. refresh-golden.sh
# therefore regenerates all of them and then undoes the ones outside its target
# scenario, so it never masks another cell's drift.
#
# Why the "before" snapshot matters (#1333 / B1). That undo used to be
# unconditional: revert every modified golden outside the scope. In a LOOP — one
# cell after another, which is exactly how a record sweep runs — each iteration
# reverted the previous one's freshly-written golden. Observed directly: a loop
# over 8 cells reported `M <golden>` for four of them, and `git status --short`
# immediately afterwards showed only the last. The other three were gone.
#
# It was safe only under strict commit-per-cell, because a committed golden is
# not a "change" to discard. That made commit-per-cell load-bearing for a reason
# the skill never gave — it justifies it as "a dirty replaydata/ makes the next
# precheck refuse". Snapshotting what was already dirty BEFORE regenerating
# removes the hidden coupling: this run may only undo what this run caused.
#
# Both functions take newline-separated lists and echo a newline-separated list.
#
#   golden_restore_list <scope> <modified_before> <modified_now>   -> git checkout --
#   golden_remove_list  <scope> <untracked_before> <untracked_now> -> rm -f

# _outside_scope_and_new <scope> <before-list> <now-list>
# Emits entries of <now-list> that are neither under <scope>/ nor present in
# <before-list>. Scope is matched with a trailing slash so a scenario whose name
# is a prefix of another ("2-6_long" vs "2-6_longer-thing") can't claim it.
# No associative arrays: macOS ships bash 3.2, and this rig runs on developer
# laptops as well as Linux CI. grep -Fxq is the portable membership test.
_outside_scope_and_new() {
  local scope="$1" before="$2" now="$3" line
  while IFS= read -r line; do
    [[ -n "$line" ]] || continue
    [[ "$line" == "$scope"/* ]] && continue          # mine — keep it
    # already dirty before I ran — not mine to undo
    if [[ -n "$before" ]] && printf '%s\n' "$before" | grep -Fxq -- "$line"; then
      continue
    fi
    printf '%s\n' "$line"
  done <<< "$now"
}

golden_restore_list() { _outside_scope_and_new "$1" "$2" "$3"; }
golden_remove_list()  { _outside_scope_and_new "$1" "$2" "$3"; }
