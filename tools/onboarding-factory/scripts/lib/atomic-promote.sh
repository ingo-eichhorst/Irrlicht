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
# manifest.json) for BOTH outcomes — a rejected recording still has a pass rate
# worth reporting.
#
# Returns: 0 promoted · 1 populate/move failed · 3 rejected by the validator.
# On any non-zero return nothing is added to <cell_dir> and no scratch remains.

atomic_promote() {
  local cell_dir="$1" rec_name="$2" populate_fn="$3" validate_fn="${4:-}"
  local scratch candidate final summary
  final="$cell_dir/recordings/$rec_name"
  scratch="$cell_dir/.promote-tmp.$$"
  candidate="$scratch/recordings/$rec_name"

  rm -rf "$scratch"
  mkdir -p "$candidate" || return 1

  if ! "$populate_fn" "$candidate"; then
    rm -rf "$scratch"
    return 1
  fi

  if [[ -n "$validate_fn" && -f "$cell_dir/expected.jsonl" ]]; then
    cp "$cell_dir/expected.jsonl" "$scratch/expected.jsonl"
    if summary="$("$validate_fn" "$scratch" "$rec_name")"; then
      [[ -n "$summary" ]] && echo "$summary"
    else
      rm -rf "$scratch"
      [[ -n "$summary" ]] && echo "$summary"
      return 3
    fi
  fi

  mkdir -p "$cell_dir/recordings"
  if ! mv "$candidate" "$final"; then
    rm -rf "$scratch"
    return 1
  fi
  rm -rf "$scratch"
  return 0
}
