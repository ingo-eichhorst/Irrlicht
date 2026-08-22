#!/usr/bin/env bash
# mutate.sh — apply one deliberate, reversible mutation to a tracked file, run
# a test command against it, capture the (expected-red) output, and restore
# the file byte-for-byte. This is the harness `docs/testing-philosophy.md`
# requires for anything a change ADDS (a guard, a static rule, a derived
# count, a schema constraint, ...): mutate the thing it protects, confirm the
# check goes red, then leave the tree exactly as it was.
#
# WHY THIS EXISTS (#1750). Three separate agents, across #1740/#1741/#1736,
# each hand-wrote the same helper: assert the anchor text matches exactly
# once, apply, run a test selector, restore, print `git status --porcelain`.
# One of them wrote it as a Python script and noted it was "the third or
# fourth time" — the mechanics get re-improvised every run, and the part that
# gets forgotten is the stale-anchor guard (#1390): a pattern that no longer
# matches must not silently mutate nothing and let the test's pre-existing
# state read as "the guard works" (#1450). A committed tool enforces every
# guard by construction instead of by whoever remembers this time.
#
# Usage:
#   tools/mutate.sh <file> <anchor> <replacement> <test-cmd> [test-cmd-args...]
#
#   <file>          path to the tracked file to mutate (repo-relative or
#                   absolute; run this from wherever <test-cmd>'s own paths
#                   expect, same as you would run that command directly).
#   <anchor>        literal text (NOT a regex/glob) that must occur in <file>
#                   EXACTLY ONCE. May span multiple lines — pass it via a
#                   variable built with `$'...'` or `"$(cat anchor.txt)"`,
#                   never typed inline with embedded newlines.
#   <replacement>   literal text to splice in place of <anchor>. Must differ
#                   from <anchor> (byte-for-byte) — a replacement identical to
#                   the anchor is a no-op mutation and is refused.
#   <test-cmd> ...  the test command's OWN argv, executed directly (no shell
#                   re-parsing, no injection risk): `go test ./pkg/... -run
#                   TestFoo -v`, `bash tools/lib/foo_test.sh`, etc. If you need
#                   shell features (pipes, &&, globbing) inside it, pass
#                   `bash -c '...'` explicitly as the argv.
#
# Example:
#   ANCHOR=$'if x > 0 {\n\treturn 1\n}'
#   REPLACEMENT=$'if x > 0 {\n\treturn 999\n}'
#   tools/mutate.sh core/foo.go "$ANCHOR" "$REPLACEMENT" \
#     go test ./core/... -run TestBar -v
#
# What it does, in order — every one of these is a guard from the issue, not
# an implementation detail:
#   1. refuses unless <anchor> occurs EXACTLY ONCE in <file> (#1390) — zero
#      matches is reported as STALE (a literal, greppable word: #1450 wants a
#      stale anchor to be loud, never a silent no-op), more than one match is
#      refused as ambiguous;
#   2. refuses if <replacement> is byte-identical to <anchor> (the mutation
#      would leave the file unchanged);
#   3. applies the mutation, runs <test-cmd>, captures its combined output and
#      exit status;
#   4. restores <file> by COPY-BACK from a byte-for-byte backup — never `git
#      checkout --`/`git restore`/`git reset --hard`, which this repo forbids
#      everywhere: worktrees share the parent repo's .git dir, so the stash
#      and (for reset --hard) other shared state are not isolated per
#      worktree, and a restore must not touch any of it;
#   5. asserts `git status --porcelain` is empty afterward and fails LOUDLY
#      (not silently) if it is not;
#   6. prints the whole thing in the shape already used, by hand, for pasting
#      into a PR body:
#        === mutation applied to <file> ===
#        <captured test output>
#        === <test-cmd> exit=<rc> ===
#        git status after restore: '<porcelain output, empty on success>'
#
# Exit codes (every refusal explains itself on stderr; nothing here is silent):
#   0  success — mutation applied, test ran, file restored, tree clean.
#   1  usage error (wrong number of arguments, or --help).
#   2  <file> does not exist, is not a regular file, or is not read/writable.
#   3  could not read <file> byte-for-byte (embedded NUL, or the 0x01 byte
#      this tool reserves as an internal record separator — measured, not
#      assumed: file size and what was read disagree).
#   4  precondition refused — `git status --porcelain` was already non-empty
#      BEFORE mutating. The post-restore emptiness check could prove nothing
#      against a tree that started dirty; commit or clean up first.
#   5  <anchor> is empty. An empty anchor "matches everywhere" and asserts
#      nothing.
#   6  <replacement> is byte-identical to <anchor> — a no-op mutation.
#   7  STALE — <anchor> occurs ZERO times in <file> (#1390/#1450).
#   8  ambiguous — <anchor> occurs MORE THAN ONCE in <file>.
#   9  FATAL — `git status --porcelain` is NOT empty after restoring. The
#      restore itself did not leave the tree clean; this should be
#      unreachable, and reaching it is reported loudly rather than papered
#      over.
#
# Implementation notes:
#   - Literal text, not regex/glob. <anchor>/<replacement> are matched and
#     spliced as exact byte sequences via awk's index()/substr(), reached
#     through ENVIRON rather than -v — -v applies backslash-escape processing
#     to its value (so a literal `\n` inside a real anchor would silently
#     become a newline), ENVIRON does not.
#   - The whole file is read as ONE awk record via `RS="\x01"` (a byte
#     practically never present in source text) so a multi-line anchor is
#     matched as ONE literal string rather than reassembled line by line —
#     the latter would also silently normalise a missing final newline.
#     Guard #3 above is what keeps this safe if that byte, or a NUL, actually
#     shows up: the read is measured against `wc -c`, not assumed correct.
set -uo pipefail

self_help() {
  awk 'NR>1 && /^#/ {sub(/^# ?/, ""); print; next} NR>1 {exit}' "$0"
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  self_help
  exit 0
fi

if [[ $# -lt 4 ]]; then
  echo "usage: tools/mutate.sh <file> <anchor> <replacement> <test-cmd> [test-cmd-args...]" >&2
  echo "       tools/mutate.sh --help" >&2
  exit 1
fi

FILE="$1"
ANCHOR="$2"
REPLACEMENT="$3"
shift 3
TEST_CMD=("$@")

refuse() {
  local code="$1" msg="$2"
  echo "mutate: refusing — $msg" >&2
  exit "$code"
}

# ─── Guard: the file itself ────────────────────────────────────────────────
if [[ ! -f "$FILE" ]]; then
  refuse 2 "'$FILE' does not exist or is not a regular file"
fi
if [[ ! -r "$FILE" || ! -w "$FILE" ]]; then
  refuse 2 "'$FILE' is not both readable and writable"
fi

# ─── Guard: the file can be read byte-for-byte through the RS="\x01" slurp ──
# Measured, not assumed (AGENTS.md: "a validator that can't parse its input
# checks MORE, never less"). A mismatch means the file contains a NUL byte
# (many awk implementations truncate C-style strings there) or literally the
# 0x01 byte this tool reserves as its own record separator — either way,
# proceeding would silently operate on a truncated/garbled read.
FILE_BYTES="$(LC_ALL=C wc -c < "$FILE" | tr -d ' ')"
SLURPED_LEN="$(awk 'BEGIN{RS="\x01"}{print length($0)}' "$FILE")"
if [[ "$SLURPED_LEN" != "$FILE_BYTES" ]]; then
  refuse 3 "could not read '$FILE' byte-for-byte: it is $FILE_BYTES bytes but this tool's exact-byte read measured '$SLURPED_LEN' — likely an embedded NUL byte or this tool's own 0x01 sentinel. Refusing rather than risk a corrupted mutation."
fi

# ─── Guard: precondition — the tree must already be clean ──────────────────
# The postcondition below (git status --porcelain empty AFTER restoring) can
# only mean something if it was ALSO empty before this run touched anything —
# otherwise pre-existing changes elsewhere in the tree make the postcondition
# vacuous, and a genuine restore failure could hide behind them.
PRE_STATUS="$(git status --porcelain 2>&1)"
if [[ -n "$PRE_STATUS" ]]; then
  refuse 4 "git status --porcelain is already non-empty BEFORE mutating anything. Commit or clean up first — a tree that starts dirty can never prove the post-restore emptiness check meant anything:
$PRE_STATUS"
fi

# ─── Guard: the anchor and replacement ─────────────────────────────────────
if [[ -z "$ANCHOR" ]]; then
  refuse 5 "the anchor is empty — an empty anchor matches everywhere and asserts nothing"
fi
if [[ "$ANCHOR" == "$REPLACEMENT" ]]; then
  refuse 6 "the replacement is byte-identical to the anchor — this mutation would leave '$FILE' UNCHANGED"
fi

# ─── Guard: the anchor occurs EXACTLY ONCE (#1390) ─────────────────────────
COUNT="$(MUTATE_ANCHOR="$ANCHOR" awk '
  BEGIN { RS = "\x01" }
  {
    content = $0
    anchor = ENVIRON["MUTATE_ANCHOR"]
    alen = length(anchor)
    n = 0
    pos = 1
    while (1) {
      idx = index(substr(content, pos), anchor)
      if (idx == 0) break
      n++
      pos = pos + idx - 1 + alen
    }
    print n
  }
' "$FILE")"

if [[ "$COUNT" -eq 0 ]]; then
  refuse 7 "STALE — the anchor occurs 0 times in '$FILE'. A stale anchor that no longer matches must not silently do nothing (#1390); update the anchor to match the current text."
fi
if [[ "$COUNT" -gt 1 ]]; then
  refuse 8 "the anchor occurs $COUNT times in '$FILE', not exactly once — an ambiguous anchor could mutate the wrong occurrence. Widen it with more surrounding context until it is unique."
fi

# ─── Backup, apply, and an EXIT trap so a restore always happens ───────────
BACKUP="$(mktemp "${TMPDIR:-/tmp}/mutate.XXXXXX")"
cp -p "$FILE" "$BACKUP"

RESTORED=0
restore_file() {
  # Idempotent: safe to call more than once (the trap calls it as a safety
  # net even after the main flow already restored explicitly).
  if [[ "$RESTORED" -eq 0 && -f "$BACKUP" ]]; then
    cp -p "$BACKUP" "$FILE"
    rm -f "$BACKUP"
    RESTORED=1
  fi
}
trap restore_file EXIT

MUTATE_ANCHOR="$ANCHOR" MUTATE_REPLACEMENT="$REPLACEMENT" awk '
  BEGIN { RS = "\x01" }
  {
    content = $0
    anchor = ENVIRON["MUTATE_ANCHOR"]
    repl = ENVIRON["MUTATE_REPLACEMENT"]
    alen = length(anchor)
    idx = index(content, anchor)
    prefix = substr(content, 1, idx - 1)
    suffix = substr(content, idx + alen)
    printf "%s", prefix repl suffix
  }
' "$FILE" > "$BACKUP.mutated"
cat "$BACKUP.mutated" > "$FILE"
rm -f "$BACKUP.mutated"

echo "=== mutation applied to $FILE ==="

# ─── Run the test command, capturing output and exit status ───────────────
TEST_OUTPUT="$("${TEST_CMD[@]}" 2>&1)"
TEST_RC=$?

printf '%s\n' "$TEST_OUTPUT"
printf '=== %s exit=%s ===\n' "$(printf '%q ' "${TEST_CMD[@]}")" "$TEST_RC"

# ─── Restore, then assert the tree is provably clean ───────────────────────
restore_file

POST_STATUS="$(git status --porcelain 2>&1)"
echo "git status after restore: '$POST_STATUS'"
if [[ -n "$POST_STATUS" ]]; then
  echo "mutate: FATAL — git status --porcelain is NOT empty after restoring '$FILE'. The restore did not leave the tree clean:" >&2
  echo "$POST_STATUS" >&2
  exit 9
fi

exit 0
