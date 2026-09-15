#!/usr/bin/env bash
# atomic-promote.sh — build a recording, validate it, and only THEN put it in the
# tree.
#
# Sourced by tools/promote-recording.sh.
#
# Why (#1333, finding B2). promote-recording.sh used to `mkdir -p` the FINAL
# recordings/<name>/ dir, copy into it, and only afterwards run
# expected-validate — so a spec violation printed "the recording is in place but
# the validator is unhappy", exited 3, and left the bad recording committed to
# the tree. Recovery was a manual `rm -rf` before re-promoting (hit on
# 2-8_autonomous-loop-iteration-limit, which promoted at 3/4 before the spec was
# corrected). `of validate` won't catch the leftover either — it gates recording
# COMPLETENESS (events, manifest, transcript, golden), never the manifest's pass
# rate, so a partially-passing recording sits in the tree looking complete.
#
# The candidate is staged inside a scratch dir shaped like a cell:
#
#   <cell>/.promote-tmp.<pid>/expected.jsonl
#   <cell>/.promote-tmp.<pid>/recordings/<name>/…
#
# because expected-validate's contract is `<cell-dir> <recording-name>` — it
# resolves <cell-dir>/recordings/<name>, so the candidate has to be reachable
# that way before it is real. The scratch dir is a sibling of the target (same
# filesystem), so the final `mv` is a rename, not a copy. It is dot-prefixed so
# `recordings/*/` globs and Go's NewestRecordingDir can never see a candidate.
#
# atomic_promote <cell_dir> <rec_name> <populate_fn> [<validate_fn>]
#   populate_fn <dir>             fills the candidate recording dir; non-zero aborts
#   validate_fn <cell_dir> <name> echoes a summary; non-zero rejects the candidate
#
# Echoes the validator's summary on stdout (the caller stamps it into
# manifest.json) for EVERY outcome that reaches the validator — a rejected or
# known-failing-promoted recording still has a pass rate worth reporting.
#
# known_failing exception (#1967). A candidate that fails validation is
# normally refused outright (see B2 above). But a cell whose OWN committed
# expected.jsonl meta line already declares `known_failing:true` is meant to
# end at a sub-100% pass rate — record/SKILL.md Step 3 documents this: the
# recording is real captured data, not a broken run, so it still commits.
# Before this exception, agents hit the gate's blanket refusal here 6+ times
# (muse onboarding alone: 5 separate cells) and worked around it by trimming
# expected.jsonl to only the passing phases, promoting, then restoring the
# full spec — the same detour the pre-existing aider/2-15_shell-escape-command
# cell shows signs of. The flag is read straight from <cell_dir>/expected.jsonl
# — never a caller-supplied bypass flag, which could disagree with what is
# actually committed — so the decision always tracks the exact bytes the
# validator just graded. A cell NOT marked known_failing keeps the original
# blanket refusal: see atomic-promote_test.sh's "validation fails" case (the
# lock) and atomic-promote-mutations_test.sh, which breaks this exact
# discrimination and confirms that lock catches it.
#
# Returns: 0 promoted · 1 populate/move failed · 2 promoted despite failing
# validation (expected.jsonl declares known_failing:true) · 3 rejected by the
# validator. On a 1 or 3 return nothing is added to <cell_dir> and no scratch
# remains. A 2 return promotes exactly like 0 (the caller still gets the
# failing summary as the pass rate to stamp into manifest.json) — callers MUST
# print something distinct for 2 so a deliberate known-failing promote never
# reads the same as a clean pass.

atomic_promote() {
  local cell_dir="$1" rec_name="$2" populate_fn="$3" validate_fn="${4:-}"
  local scratch candidate final summary
  # promote_rc carries the known_failing exception (#1967) through to the
  # final return: the branch below sets it to 2 and falls through to the SAME
  # mv logic a clean pass uses, rather than returning early like the ordinary
  # rejection does. `local promote_rc=0` — not a bare `local promote_rc` — so
  # an interrupt between here and the branch below still returns 0, not an
  # unset/empty value, if the RETURN trap's cleanup were ever reached without
  # the variable being assigned.
  local promote_rc=0
  final="$cell_dir/recordings/$rec_name"
  scratch="$cell_dir/.promote-tmp.$$"
  candidate="$scratch/recordings/$rec_name"

  # The scratch dir holds a full copy of the candidate and lives INSIDE the
  # committed tree, so an interrupt during validation (which shells out to
  # `go run`, i.e. a compile) would otherwise strand it — and `record`'s next
  # documented step is `git add <cell-dir>/`, a directory add that takes
  # dotfiles. The RETURN trap covers every exit from here on, including the
  # caller's Ctrl-C, and .gitignore carries `.promote-tmp*` as the belt.
  trap 'rm -rf "$scratch"' RETURN

  rm -rf "$scratch"
  mkdir -p "$candidate" || return 1

  if ! "$populate_fn" "$candidate"; then
    return 1
  fi

  if [[ -n "$validate_fn" && -f "$cell_dir/expected.jsonl" ]]; then
    cp "$cell_dir/expected.jsonl" "$scratch/expected.jsonl"
    # `if summary=...` rather than `[[ -n ]] && echo` on either branch: an
    # AND-list returns 1 on an empty summary, which under a caller's `set -e`
    # would abort a SUCCESSFUL promote right before the mv.
    if summary="$("$validate_fn" "$scratch" "$rec_name")"; then
      if [[ -n "$summary" ]]; then echo "$summary"; fi
    else
      if [[ -n "$summary" ]]; then echo "$summary"; fi
      # #1967: known_failing:true is a deliberate, narrow exception to B2 —
      # see the header comment. Same idiom tools/replay-fixtures.sh already
      # uses for this exact field (`head -n1 <expected.jsonl> | jq -r
      # '.known_failing // false'`); an unreadable/missing jq falls through
      # to the empty string, which compares false, so "cannot look" defaults
      # to the STRICT refusal below, never to the lenient bypass. Falls
      # through to the SAME mv logic a clean pass uses (setting promote_rc
      # rather than returning) — a non-known_failing candidate still hits the
      # unconditional `return 3` and never reaches the mv at all.
      if [[ "$(head -n1 "$cell_dir/expected.jsonl" | jq -r '.known_failing // false' 2>/dev/null || echo false)" == "true" ]]; then
        promote_rc=2
      else
        return 3
      fi
    fi
  fi

  # Never merge into an existing recording: `mv a b` with b present nests it as
  # b/a rather than failing. promote-recording.sh picks a non-colliding name
  # first, so this only fires on a race or a different caller — but the lib
  # advertises atomicity, so it defends itself.
  if [[ -e "$final" ]]; then
    echo "atomic_promote: $final already exists; refusing to merge into it" >&2
    return 1
  fi
  mkdir -p "$cell_dir/recordings"
  if ! mv "$candidate" "$final"; then
    return 1
  fi
  return "$promote_rc"
}
