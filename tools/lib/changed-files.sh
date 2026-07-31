#!/usr/bin/env bash
# changed-files.sh — the shared definition of "what this branch changed", plus
# the per-scanner predicates that decide whether a given Go module or web tree
# is in scope for that change.
#
# It exists so the pre-push hook's two layers of scoping cannot drift apart.
# tools/preflight.sh decides *whether* to run the security gate from the
# changed set; tools/security-scan.sh then decides *which* Go modules and web
# trees inside that gate to scan. If those two answers came from two
# hand-rolled diffs they could disagree about what "changed" means — the same
# class of mismatch issue #1213 is about.
#
# This file is sourced, not executed: it defines functions and returns.
#
#   . "$SCRIPT_DIR/lib/changed-files.sh"
#   CHANGED_FILES=$(changed_files_vs_origin_main)
#   go_module_touched core "$CHANGED_FILES" && ...
#
# Every function here is pure string handling over a caller-supplied set (only
# changed_files_vs_origin_main touches git), so tools/lib/changed-files_test.sh
# can exercise the scoping rules without running a scanner.

# changed_files_vs_origin_main — everything this branch touches relative to
# origin/main: committed since the merge base, unstaged, and staged. Emits
# newline-separated repo-relative paths, sorted and de-duplicated. Uncommitted
# work counts because a pre-push run should reflect the tree in front of you,
# not only what is already committed.
#
# When origin/main has never been fetched the diff yields nothing, which is
# byte-for-byte indistinguishable from "this branch changed nothing" — and an
# empty set scopes every gate down to a skip. An empty diff is a legitimate
# state, so this warns rather than failing, but it warns loudly: a caller that
# skipped everything should be able to tell which of the two reasons applied.
changed_files_vs_origin_main() {
  if ! git rev-parse --verify --quiet origin/main >/dev/null; then
    echo "WARN: origin/main not found — treating this branch as unchanged, so every scoped gate will skip. Run 'git fetch origin main' for a meaningful scoped run." >&2
  fi
  local base
  base=$(git merge-base origin/main HEAD 2>/dev/null || echo origin/main)
  {
    git diff --name-only "$base" HEAD
    git diff --name-only HEAD
    git diff --name-only --cached
  } 2>/dev/null | sort -u
}

# go_module_touched <module-dir> <changed-files> — true when the changed set
# contains something govulncheck/gosec actually read for this module: a Go
# source file, or the module graph (go.mod/go.sum).
#
# Matching the scanner's own inputs rather than "any path under the module" is
# what keeps the nested tree honest: tools/onboarding-factory/internal/viewer/web
# lives *inside* the tools/onboarding-factory module, so a lockfile-only change
# there must not drag in a full Go vulnerability scan.
go_module_touched() {
  local mod="$1" file
  while IFS= read -r file; do
    [[ "$file" == "$mod"/* ]] || continue
    case "$file" in
      *.go | "$mod"/go.mod | "$mod"/go.sum) return 0 ;;
    esac
  done <<<"$2"
  return 1
}

# web_tree_touched <tree-dir> <changed-files> — true when the changed set
# contains what `npm audit` actually reads for this tree: its manifest or its
# lockfile. A diff that changes neither cannot alter the resolved dependency
# graph, so it cannot introduce (or clear) an advisory there.
web_tree_touched() {
  local tree="$1" file
  while IFS= read -r file; do
    case "$file" in
      "$tree"/package.json | "$tree"/package-lock.json) return 0 ;;
    esac
  done <<<"$2"
  return 1
}
