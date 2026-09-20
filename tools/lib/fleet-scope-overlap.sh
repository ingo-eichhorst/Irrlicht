#!/usr/bin/env bash
# fleet-scope-overlap.sh — decide whether two or more queued tickets may run
# side by side, by comparing the file scopes their issue bodies declare.
#
# WHY THIS EXISTS (#2022). Wave 1 of the fleet run on 2026-09-20 picked
# #2004, #2005 and #2006 because they touch disjoint trees. No conflict
# occurred. That choice was a person reading three issue bodies and holding
# the answer in their head, which is not a thing a later run can repeat or
# check. This file makes the same comparison mechanical.
#
# It reads issue BODIES FROM FILES rather than calling `gh` itself, for the
# reason tools/lib/rebase-conflict-check.sh gives for taking an explicit file
# list: a checker that fetches its own input cannot be driven from committed
# fixtures, and a checker nothing re-runs is a claim rather than a guard. The
# caller fetches, this decides:
#
#   gh issue view 2004 --repo ingo-eichhorst/Irrlicht --json body --jq .body \
#     > "$SCRATCH/fleet-2004-body.md" || exit 2
#   tools/lib/fleet-scope-overlap.sh "$SCRATCH"/fleet-*-body.md
#
# WHERE THE SCOPE COMES FROM. Section `## 5.` of the wave ticket template,
# anchored by its NUMBER and never by its words. The title is not stable —
# #2022 calls it "Affected files" while #2003, #2004 and #2005 call it
# "Affected ports, adapters, clients and stored formats" (read 2026-09-20 via
# `gh issue view <N> --json body`). The number is stable across all five.
#
# UNDECLARED IS THE COMMON CASE, NOT AN EDGE CASE. Of the nine open
# ready-for-agent issues on 2026-09-20, six carried a `## 5.` section and
# three did not (counted with `gh issue list --label ready-for-agent` piped
# through a grep for the heading). A ticket that declares no scope is
# therefore reported UNDECLARED and exits 2 — it is dispatchable solo and
# never beside another ticket. It is never reported as disjoint, because
# "these two do not collide" and "I could not tell" are different answers and
# only one of them is safe to dispatch on.
#
# HOW A TOKEN BECOMES A KEY. Review of #2022 broke the first version of this
# reduction four separate ways, each reproduced on a live issue body and each
# now a committed fixture. The rules exist in this order for those reasons:
#
#   1. Iterate with `while IFS= read -r`, never `for t in $(...)`. Unquoted
#      expansion glob-matched the tokens against the filesystem: #2022's own
#      `.claude/skills/**/*.md` became eleven keys it never declared, and the
#      SAME two inputs gave OVERLAP from the repo root and DISJOINT from
#      anywhere else. `set -f` is not the fix — this file is sourced, and the
#      option would leak into the caller.
#   2. Strip a `:86` line reference BEFORE classifying, not after. The first
#      version stripped afterwards, where no input could ever reach it.
#   3. A token carrying a glob character reduces to its longest glob-free
#      directory prefix, as a tree. `.claude/skills/**/*.md` becomes the
#      `.claude/skills/` tree: wider than the ticket said, which is the safe
#      direction.
#   4. A token with a "/" but no known file extension is a DIRECTORY, so it
#      becomes a tree rather than going through `dirname`. #2008 declares
#      "A new `internal/provider` package"; `dirname` turned that into
#      `internal`, a key both wider than intended and — being file-derived —
#      unable to contain `internal/provider/resolver.go`. Two tickets editing
#      one new package reported DISJOINT.
#
# THE TWO KEY KINDS, and why the distinction is load-bearing. A tree key
# contains everything beneath it. A directory key derived from a FILE matches
# only an identical directory key — a file in `core/adapters/` and a file in
# `core/adapters/inbound/agents/pi/` live in different packages and do not
# collide on rebase, so a parent directory derived from a file must never
# swallow its children. That restriction is pinned by the
# `adapters-root-file.md` / `pi-adapter.md` pair, which reports DISJOINT and
# reports OVERLAP as soon as containment is widened to every key.
#
# Known limits, deliberate:
#   * A token with no "/" is a key only when it carries a known extension or
#     is one of the extensionless root files below. Anything else is a prose
#     span (`muse`, `handleNonMessageEvent`, `of`) and is reported on an
#     IGNORED line rather than dropped in silence — a validator that cannot
#     parse its input must say so, never quietly check less.
#   * A bullet that DISCLAIMS a scope ("No change to `core/domain`", live in
#     #2010 and #2013) is skipped by a small negation test that reads only
#     the text before the bullet's first backtick. A disclaimer phrased any
#     other way still counts as a declaration, and over-reports.
#   * A ticket naming a tree it does not really touch in full over-reports,
#     and the answer is to dispatch it solo rather than to guess.
#     Over-reporting costs a wave; under-reporting costs a conflict.
#
# Usage:
#   tools/lib/fleet-scope-overlap.sh <issue-body-file> [issue-body-file...]
#
# Sourced form, which defines the function and runs nothing:
#   . tools/lib/fleet-scope-overlap.sh
#   fleet_scope_overlap body-a.md body-b.md
#
# Exit codes:
#   0  every named ticket declares a scope, and every pair is disjoint
#   1  FINDING — at least one pair's declared scopes overlap
#   2  REFUSAL — could not decide. Two distinct causes, and the caller must
#      read the lines to tell them apart: an UNDECLARED ticket runs solo,
#      while a REFUSE line means the input itself could not be read and the
#      run stops. A refusal outranks a finding, matching
#      tools/lib/rebase-conflict-check.sh.
set -uo pipefail

# Extensionless files that really are files. Without these, `go.work` was
# dropped, and two tickets bumping a Go directive reported DISJOINT.
FLEET_SCOPE_BARE_FILES='go.work go.work.sum go.mod go.sum Makefile Dockerfile LICENSE CODEOWNERS'

# Extensions that mark a token as a FILE rather than a directory. Named once
# because the test is applied at two sites — a token with a "/" and a token
# without one — and an extension added to only one of them misclassifies the
# other silently: bare `foo.toml` would key as a file while `dir/foo.toml`
# keyed as the directory tree `dir/foo.toml/`.
FLEET_SCOPE_FILE_EXTS='md json sh go yml yaml swift js ts html css rs py txt'

# fleet_scope_is_file <token> — true when the token ends in a known extension.
fleet_scope_is_file() {
  case " $FLEET_SCOPE_FILE_EXTS " in
    *" ${1##*.} "*) return 0 ;;
  esac
  return 1
}

# fleet_scope_overlap <issue-body-file> [issue-body-file...]
fleet_scope_overlap() {
  if [ "$#" -eq 0 ]; then
    echo "REFUSE: fleet-scope-overlap — no files named" >&2
    return 2
  fi

  local refused=0 found=0
  local scopes=""
  local path label section line prefix token key keys key_list ignored base

  for path in "$@"; do
    if [ -d "$path" ]; then
      echo "REFUSE: fleet-scope-overlap — '$path' is a directory, not a file" >&2
      refused=1
      continue
    fi
    if [ ! -r "$path" ]; then
      echo "REFUSE: fleet-scope-overlap — cannot read '$path'" >&2
      refused=1
      continue
    fi

    label=${path##*/}

    # Section 5 by number. The terminator is the next numbered `## ` heading,
    # so renaming section 6 cannot silently swallow section 5.
    section=$(awk '
      /^## 5\./ { armed = 1; next }
      armed && /^## [0-9]+\./ { exit }
      armed { print }
    ' "$path")

    # A leading newline so the dedup test can anchor both ends. Without it
    # `agents` would dedup against `replaydata/agents`.
    keys=$'\n'
    ignored=""

    while IFS= read -r line; do
      [ -n "$line" ] || continue

      # A bullet that disclaims a scope is not a declaration. Read only the
      # text before the first backtick, so "No change to `core/domain`" is
      # skipped while a path followed by prose is not.
      #
      # A disclaimer bullet carrying MORE than one path is ambiguous: review
      # of #2022 showed "- No change to `core/domain`; `tools/lib/helper.sh`
      # gains the guard." dropping the second path in silence, which cleared
      # two tickets that both edit tools/lib to share a wave. Skipping one
      # token is a reading; skipping a declaration is a wrong answer in the
      # unsafe direction. So a single-token disclaimer is skipped and
      # anything longer refuses the ticket.
      prefix=${line%%\`*}
      case "$prefix" in
        *[Nn]o\ change* | *[Nn]o\ file* | *[Nn]o\ new\ file*)
          if [ "$(printf '%s\n' "$line" | grep -o '`[^`]*`' | grep -cv '[[:space:]]')" -gt 1 ]; then
            echo "AMBIGUOUS: $label — a disclaiming bullet also names another path, so its scope cannot be read: $line" >&2
            refused=1
          fi
          continue
          ;;
      esac

      while IFS= read -r token; do
        [ -n "$token" ] || continue

        # Strip a trailing `:86` line reference FIRST, so classification sees
        # the real path.
        case "$token" in
          *:[0-9] | *:[0-9][0-9] | *:[0-9][0-9][0-9] | *:[0-9][0-9][0-9][0-9])
            token=${token%:*}
            ;;
        esac
        [ -n "$token" ] || continue

        case "$token" in
          *'*'* | *'?'* | *'['*)
            # Longest glob-free prefix, trimmed to a directory, as a tree.
            base=${token%%[*?[]*}
            case "$base" in
              */*) key="${base%/*}/" ;;
              *) ignored="$ignored $token"; continue ;;
            esac
            ;;
          */)
            key="${token%/}/"
            ;;
          */*)
            if fleet_scope_is_file "$token"; then
              # `${token%/*}` rather than `dirname`: the enclosing `*/*` case
              # guarantees a slash, so the two agree, and this forks nothing.
              key=${token%/*}
            else
              # A path with no known extension names a directory.
              key="$token/"
            fi
            ;;
          *)
            if fleet_scope_is_file "$token"; then
              key="$token"
            else
              case " $FLEET_SCOPE_BARE_FILES " in
                *" $token "*) key="$token" ;;
                *) ignored="$ignored $token"; continue ;;
              esac
            fi
            ;;
        esac

        case "$keys" in
          *$'\n'"$key"$'\n'*) ;;
          *) keys="$keys$key"$'\n' ;;
        esac
      done <<<"$(printf '%s\n' "$line" | grep -o '`[^`]*`' | tr -d '`' | grep -v '[[:space:]]')"
    done <<<"$section"

    if [ -n "$ignored" ]; then
      echo "IGNORED: $label —$ignored (no path shape; named so the drop is not silent)"
    fi

    if [ "$keys" = $'\n' ]; then
      if [ -z "$section" ]; then
        echo "UNDECLARED: $label — no '## 5.' section; solo only, never co-dispatched" >&2
      else
        echo "UNDECLARED: $label — '## 5.' section names no file path; solo only, never co-dispatched" >&2
      fi
      refused=1
      continue
    fi

    # One conversion, read twice. The two sites ran the same pipeline on the
    # same value.
    key_list=${keys//$'\n'/ }
    echo "DECLARED: $label —$key_list"
    scopes="$scopes$label|$key_list"$'\n'
  done

  # Compare every pair of tickets that declared a scope.
  local a b a_label b_label a_keys b_keys b_lines ka kb ka_base kb_base shared
  local i=0 j
  while IFS= read -r a; do
    [ -n "$a" ] || continue
    i=$((i + 1))
    j=0
    while IFS= read -r b; do
      [ -n "$b" ] || continue
      j=$((j + 1))
      [ "$j" -gt "$i" ] || continue
      a_label=${a%%|*}
      b_label=${b%%|*}
      a_keys=${a#*|}
      b_keys=${b#*|}
      shared=""
      b_lines=${b_keys// /$'\n'}
      while IFS= read -r ka; do
        [ -n "$ka" ] || continue
        while IFS= read -r kb; do
          [ -n "$kb" ] || continue
          ka_base=${ka%/}
          kb_base=${kb%/}
          if [ "$ka_base" = "$kb_base" ]; then
            shared="$shared $ka_base"
            continue
          fi
          # A tree key contains everything beneath it. A directory key
          # derived from a file contains nothing.
          case "$ka" in
            */) [ "${kb_base#"$ka_base"/}" != "$kb_base" ] && shared="$shared $ka_base/>$kb_base" ;;
          esac
          case "$kb" in
            */) [ "${ka_base#"$kb_base"/}" != "$ka_base" ] && shared="$shared $kb_base/>$ka_base" ;;
          esac
        done <<<"$b_lines"
      done <<<"${a_keys// /$'\n'}"
      if [ -n "$shared" ]; then
        echo "OVERLAP: $a_label vs $b_label —$shared"
        found=1
      else
        echo "DISJOINT: $a_label vs $b_label"
      fi
    done <<<"$scopes"
  done <<<"$scopes"

  if [ "$refused" -eq 1 ]; then
    echo "REFUSE: fleet-scope-overlap — at least one ticket's scope could not be decided (see UNDECLARED/REFUSE lines)" >&2
    return 2
  fi
  if [ "$found" -eq 1 ]; then
    echo "FAIL: fleet-scope-overlap — overlapping scopes must not run in the same wave (see OVERLAP lines)" >&2
    return 1
  fi
  echo "OK: fleet-scope-overlap — every pair disjoint in: $*"
  return 0
}

# Only run the CLI form when executed directly — sourcing (the test file does
# `. tools/lib/fleet-scope-overlap.sh`) must define the function and return
# control, never run the check or exit the caller's shell.
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  fleet_scope_overlap "$@"
  exit $?
fi
