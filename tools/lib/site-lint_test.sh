#!/usr/bin/env bash
# site-lint_test.sh — mutation evidence for tools/site-lint.sh (#1894).
#
# Per this repo's testing philosophy: a check a change ADDS has no "before the
# fix" to run red against, so it owes a deliberate mutation instead. The
# mutations live in tools/lib/testdata/site-lint/ as two complete miniature
# sites, so the evidence outlives this PR and nothing has to break the real
# site/ to prove a check works.
#
#   clean/   every check satisfied            -> exit 0, zero findings
#   broken/  exactly one instance of each     -> exit 1, each finding by name
#
# The broken corpus carries, one apiece:
#   dead-local-link   index.html -> docs/absent.html
#   unclosed-tag      index.html leaves <main> open
#   missing-head      index.html has no <title>; docs/page.html no canonical
#   sitemap-dangling  sitemap names docs/never-existed.html
#   sitemap-missing   docs/unlisted.html is absent from the sitemap  (WARN)
#
# Plus two refusals and a vacuity guard. The refusals matter most: this script
# first shipped dying under `set -euo pipefail` when a grep matched nothing,
# which printed NOTHING and exited 1 — a linter that cannot run reporting
# exactly what a failing site reports. "read N file(s)" in the output is the
# assertion that it looked at something, and the tests below read it.
set -uo pipefail

REPO_ROOT=$(git rev-parse --show-toplevel)
cd "$REPO_ROOT" || { echo "FAIL: cannot cd to repo root $REPO_ROOT" >&2; exit 1; }

LINT=tools/site-lint.sh
CORPUS=tools/lib/testdata/site-lint
rc=0
fail() {
  local why="$1"
  echo "FAIL: $why" >&2
  rc=1
  return 0
}

STAGE=$(mktemp -d)
trap 'rm -rf "$STAGE"' EXIT

# stage <name> — copy one committed corpus into $STAGE/<name>, stripping the
# .in suffix, and echo the path. It asserts the file count it wrote: a staging
# bug that produced an empty tree would otherwise hand the linter nothing, and
# "the site is clean" and "there was no site" must not read alike.
stage() {
  local name="$1" src dest src_count dest_count rel
  src="$CORPUS/$name"
  dest="$STAGE/$name"
  src_count=$(find "$src" -type f -name '*.in' | wc -l | tr -d ' ')
  if [[ "$src_count" -eq 0 ]]; then
    echo "FAIL: no .in fixture under $src — the corpus is missing" >&2
    exit 1
  fi
  while IFS= read -r file; do
    rel="${file#"$src"/}"
    mkdir -p "$dest/$(dirname "$rel")"
    cp "$file" "$dest/${rel%.in}"
  done < <(find "$src" -type f -name '*.in')
  dest_count=$(find "$dest" -type f | wc -l | tr -d ' ')
  if [[ "$dest_count" -ne "$src_count" ]]; then
    echo "FAIL: staged $dest_count of $src_count fixture(s) from $src" >&2
    exit 1
  fi
  printf '%s\n' "$dest"
}

CLEAN=$(stage clean)
BROKEN=$(stage broken)

# run_lint <site-dir> [extra args...] -> sets OUT and GOT
run_lint() {
  OUT=$("$LINT" --site "$@" 2>&1)
  GOT=$?
  return 0
}

# --- clean corpus passes ------------------------------------------------
run_lint "$CLEAN"
[[ "$GOT" -eq 0 ]] || fail "the clean corpus must pass; exit=$GOT output:\n$OUT"
case "$OUT" in
  *"read 3 file(s)"*) ;;
  *) fail "the clean run must say how many files it read; got:\n$OUT" ;;
esac
case "$OUT" in
  *ERROR*|*WARN*) fail "the clean corpus must produce no finding; got:\n$OUT" ;;
  *) ;;  # clean, as required
esac

# --- broken corpus fails, once per check --------------------------------
run_lint "$BROKEN"
[[ "$GOT" -eq 1 ]] || fail "the broken corpus must fail with exit 1; exit=$GOT output:\n$OUT"

expect_finding() {
  local needle="$1" label="$2"
  case "$OUT" in
    *"$needle"*) ;;
    *) fail "the broken corpus must report $label; output:\n$OUT" ;;
  esac
  return 0
}
expect_finding 'dead local link: docs/absent.html' 'the dead local link'
expect_finding '<main> opened 1 time(s), closed 0'  'the unclosed <main>'
expect_finding 'no <title>'                          'the missing title'
expect_finding 'no rel="canonical" link'             'the missing canonical link'
expect_finding 'names /docs/never-existed.html'      'the dangling sitemap entry'
expect_finding 'does not list /docs/unlisted.html'   'the unlisted docs page'

# --strict promotes the warning; the failure count must rise by exactly one.
run_lint "$BROKEN" --strict
[[ "$GOT" -eq 1 ]] || fail "--strict must still fail; exit=$GOT"
case "$OUT" in
  *"6 failure(s)"*) ;;
  *) fail "--strict must promote the warning to a 6th failure; output:\n$OUT" ;;
esac

# --- refusals: cannot look must not read as found nothing ---------------
run_lint "$STAGE/does-not-exist"
[[ "$GOT" -eq 2 ]] || fail "a missing site directory must refuse with exit 2; exit=$GOT"
case "$OUT" in
  *"cannot run"*) ;;
  *) fail "the refusal must say the lint could not run; output:\n$OUT" ;;
esac

EMPTY="$STAGE/empty"
mkdir -p "$EMPTY"
run_lint "$EMPTY"
[[ "$GOT" -eq 2 ]] || fail "a directory with no HTML page must refuse with exit 2; exit=$GOT"
case "$OUT" in
  *"no HTML page"*) ;;
  *) fail "the refusal must name what was missing; output:\n$OUT" ;;
esac

# --- vacuity guard: the real site must pass ------------------------------
# Without this every case above could be satisfied by a lint wired to fixtures
# alone, never opening the tree it exists to protect.
OUT=$("$LINT" 2>&1); GOT=$?
[[ "$GOT" -eq 0 ]] || fail "the committed site/ must pass site-lint; exit=$GOT output:\n$OUT"
# Read the count back rather than pattern-matching a prefix: "read 2" also
# matches "read 24", which would pass this guard for the wrong reason.
read_count=$(printf '%s\n' "$OUT" | sed -n 's/.*read \([0-9][0-9]*\) file(s).*/\1/p' | tail -1)
if [[ -z "$read_count" ]]; then
  fail "the real site run must report how many files it read; output:\n$OUT"
elif [[ "$read_count" -lt 10 ]]; then
  fail "the real site run read only $read_count file(s) — the walk is broken"
fi

if [[ "$rc" -eq 0 ]]; then
  echo "site-lint_test: ALL PASS"
fi
exit "$rc"
