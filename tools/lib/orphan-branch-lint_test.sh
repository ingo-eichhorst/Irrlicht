#!/usr/bin/env bash
# Drive tools/orphan-branch-lint.sh against a throwaway bare-repo remote and
# the stub gh in tools/lib/testdata/pr-exists/gh-stub.sh (#2029).
#
# The fixture remote carries four branches besides main:
#   feat/old-orphan    one commit, dated three days ago, no PR    -> FAIL
#   feat/merged        one commit, dated three days ago, has a PR -> not named
#   feat/fresh-orphan  one commit, dated now, no PR               -> INFO only
#   feat/level         at main, no commits ahead                  -> not asked
#
# The PR table also holds a FORK PR whose head is named feat/old-orphan: gh
# matches heads by name only, so a lint that counted it would hide the orphan.
#
# The load-bearing refusal rows are gh failing and a listing that fills its
# own --limit: the lint must exit 2 and must NOT print its OK line, because
# "no orphans among the PRs it could list" is not "no orphans".
set -uo pipefail # NOT -e: assertions capture non-zero return codes

REPO_ROOT=$(git rev-parse --show-toplevel)
cd "$REPO_ROOT" || { echo "FAIL: cannot cd to repo root $REPO_ROOT" >&2; exit 1; }

LINT="$REPO_ROOT/tools/orphan-branch-lint.sh"
STUB="$REPO_ROOT/tools/lib/testdata/pr-exists/gh-stub.sh"
for f in "$LINT" "$STUB"; do
  [ -r "$f" ] || { echo "FAIL: orphan-branch-lint_test — $f not found" >&2; exit 1; }
done

rc=0
fail() { echo "FAIL: $1" >&2; rc=1; }

WORK=$(mktemp -d "${TMPDIR:-/tmp}/orphan-branch-lint.XXXXXX")
trap 'rm -rf "$WORK"' EXIT
mkdir -p "$WORK/bin"
cp "$STUB" "$WORK/bin/gh"
chmod +x "$WORK/bin/gh"

# ── Fixture remote ──────────────────────────────────────────────────────────
# Commits carry fixed identities and explicit dates so the host's git config
# and clock cannot change what the lint sees.
g() {
  GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 \
    GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@example.invalid \
    GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@example.invalid \
    git -C "$WORK/clone" "$@"
}
commit_at() { # commit_at <epoch> <message>
  printf '%s\n' "$2" >>"$WORK/clone/file.txt"
  g add file.txt
  GIT_AUTHOR_DATE="@$1 +0000" GIT_COMMITTER_DATE="@$1 +0000" g commit -qm "$2"
}

now=$(date +%s)
old=$((now - 3 * 86400))

git init -q --bare "$WORK/remote.git" || { echo "FAIL: cannot create the fixture remote" >&2; exit 1; }
git init -q "$WORK/clone" || { echo "FAIL: cannot create the fixture clone" >&2; exit 1; }
g checkout -q -b main
commit_at "$old" base
g remote add origin "$WORK/remote.git"
g branch feat/level
g checkout -q -b feat/old-orphan main && commit_at "$old" old-orphan
g checkout -q -b feat/merged main && commit_at "$old" merged
g checkout -q -b feat/fresh-orphan main && commit_at "$now" fresh-orphan
g checkout -q main
g push -q origin main feat/level feat/old-orphan feat/merged feat/fresh-orphan ||
  { echo "FAIL: cannot push the fixture branches" >&2; exit 1; }

# Vacuity guard for the fixture itself: every branch must really be on the
# remote, or the rows below would pass against a remote with nothing to find.
pushed=$(git -C "$WORK/remote.git" for-each-ref --format='%(refname:short)' refs/heads | sort | tr '\n' ' ')
if [ "$pushed" != "feat/fresh-orphan feat/level feat/merged feat/old-orphan main " ]; then
  echo "FAIL: the fixture remote holds '$pushed', not the five branches this test needs" >&2
  exit 1
fi

printf 'feat/merged\nfeat/old-orphan fork\n' >"$WORK/pr-heads"
: >"$WORK/empty.waivers"

# run_lint <out-var-prefix> [lint args...] — runs with the stub gh first on
# PATH, from inside the clone, and records exit status, stdout, stderr and the
# heads the stub was asked about. Extra environment goes in LINT_ENV.
LINT_ENV=()
run_lint() {
  : >"$WORK/queries.log"
  (cd "$WORK/clone" && env ${LINT_ENV[@]+"${LINT_ENV[@]}"} \
    PATH="$WORK/bin:$PATH" PR_EXISTS_STUB_HEADS="$WORK/pr-heads" \
    PR_EXISTS_STUB_LOG="$WORK/queries.log" \
    bash "$LINT" "$@" >"$WORK/out" 2>"$WORK/err")
  got=$?
  out=$(cat "$WORK/out")
  err=$(cat "$WORK/err")
  all="$out"$'\n'"$err"
}

show() { printf '%s\n' "$all" | sed 's/^/      | /' >&2; }

# ── 1. The orphan is named, and only the orphan fails ───────────────────────
run_lint --waivers "$WORK/empty.waivers"
if [ "$got" -ne 1 ]; then
  fail "an old orphan must exit 1, got $got"; show
else
  echo '  PASS: an orphan older than 24h exits 1'
fi
printf '%s\n' "$err" | grep -q 'ahead=1  feat/old-orphan' &&
  echo '  PASS: the old orphan is named with its ahead count' ||
  { fail 'the old orphan is not named on stderr'; show; }
printf '%s\n' "$all" | grep -q 'feat/merged' &&
  { fail 'a branch that has a PR was named'; show; } ||
  echo '  PASS: a branch with a PR is not named'
printf '%s\n' "$all" | grep -q 'feat/level' &&
  { fail 'a branch level with main was named'; show; } ||
  echo '  PASS: a branch level with main is not named'
printf '%s\n' "$out" | grep -q '^INFO:' && printf '%s\n' "$out" | grep -q 'feat/fresh-orphan' &&
  echo '  PASS: a fresh orphan is reported as INFO' ||
  { fail 'the fresh orphan is not reported as INFO'; show; }
printf '%s\n' "$err" | grep -q 'feat/fresh-orphan' &&
  { fail 'a fresh orphan was reported as a failure'; show; } ||
  echo '  PASS: a fresh orphan is not a failure'
printf '%s\n' "$all" | grep -q '^OK:' &&
  { fail 'the OK line printed on a failing run'; show; } ||
  echo '  PASS: no OK line on a failing run'

# The stub was really asked — once, for the whole listing, with no --head.
asked=$(wc -l <"$WORK/queries.log" | tr -d ' ')
if [ "$asked" = "1" ] && [ -z "$(tr -d '\n' <"$WORK/queries.log")" ]; then
  echo '  PASS: gh was asked once, for the whole listing'
else
  fail "gh was asked $asked time(s), not once for the whole listing"
fi

# ── 1b. A PR listing larger than a pipe buffer ──────────────────────────────
# The real repository has 1218 PRs. A lookup written as `printf "$heads" |
# grep -q` under pipefail reports SIGPIPE (141) when grep exits on an EARLY
# match while printf is still writing, so a branch whose PR sorts first read
# as having none. The first live run of the listing design named 10 such
# branches, feat/1798-error-state (PR #1809) among them; this row is that run,
# shrunk. feat/merged is listed first, then 20000 filler heads.
{ printf 'feat/merged\n'; i=0; while [ "$i" -lt 20000 ]; do printf 'filler/branch-%05d\n' "$i"; i=$((i + 1)); done; } >"$WORK/pr-heads-big"
cp "$WORK/pr-heads" "$WORK/pr-heads-small"
cp "$WORK/pr-heads-big" "$WORK/pr-heads"
run_lint --waivers "$WORK/empty.waivers"
cp "$WORK/pr-heads-small" "$WORK/pr-heads"
printf '%s\n' "$err" | grep -q 'feat/merged' &&
  { fail 'with 20001 PRs listed, a branch whose PR is listed FIRST was named as an orphan'; show; } ||
  echo '  PASS: a large listing still finds a PR listed first'
printf '%s\n' "$err" | grep -q 'ahead=1  feat/old-orphan' &&
  echo '  PASS: a large listing still names the real orphan' ||
  { fail 'with 20001 PRs listed, the real orphan was not named'; show; }

# ── 2. Only fresh orphans left → exit 0, with the OK line ───────────────────
printf 'feat/old-orphan  kept on purpose for this fixture\n' >"$WORK/live.waivers"
run_lint --waivers "$WORK/live.waivers"
if [ "$got" -eq 0 ] && printf '%s\n' "$out" | grep -q '^OK:'; then
  echo '  PASS: with the old orphan waived, the run is clean and says so'
else
  fail "a waived orphan plus a fresh one must exit 0 with an OK line, got $got"; show
fi
printf '%s\n' "$out" | grep -q 'feat/fresh-orphan' &&
  echo '  PASS: the fresh orphan is still listed as INFO on a clean run' ||
  { fail 'the fresh orphan vanished from a clean run'; show; }

# ── 3. The threshold is honoured ────────────────────────────────────────────
run_lint --waivers "$WORK/live.waivers" --max-age-hours 08
if [ "$got" -eq 0 ] && printf '%s\n' "$out" | grep -q '^OK:'; then
  echo '  PASS: a leading-zero --max-age-hours is read as decimal'
else
  fail "--max-age-hours 08 must be read as 8 hours and exit 0, got $got"; show
fi

run_lint --waivers "$WORK/live.waivers" --max-age-hours 0
if [ "$got" -eq 1 ] && printf '%s\n' "$err" | grep -q 'feat/fresh-orphan'; then
  echo '  PASS: --max-age-hours 0 turns the fresh orphan into a failure'
else
  fail "--max-age-hours 0 must fail on the fresh orphan, got $got"; show
fi

# ── 4. A stale waiver fails ─────────────────────────────────────────────────
printf 'feat/old-orphan  kept on purpose\nfeat/long-gone  deleted months ago\n' >"$WORK/stale.waivers"
run_lint --waivers "$WORK/stale.waivers"
if [ "$got" -eq 1 ] && printf '%s\n' "$err" | grep -q 'feat/long-gone'; then
  echo '  PASS: a waiver naming no branch fails and is named'
else
  fail "a stale waiver must exit 1 and be named, got $got"; show
fi

# A waiver on a branch that has caught up with main is stale too.
printf 'feat/level  used to carry work\nfeat/old-orphan  kept on purpose\n' >"$WORK/level.waivers"
run_lint --waivers "$WORK/level.waivers"
if [ "$got" -eq 1 ] && printf '%s\n' "$err" | grep -q 'feat/level'; then
  echo '  PASS: a waiver on a branch no longer ahead of main fails'
else
  fail "a waiver on a level branch must exit 1 and be named, got $got"; show
fi

# ── 5. Refusals — could not look must never read as "no orphans" ────────────
LINT_ENV=(PR_EXISTS_STUB_FAIL=1)
run_lint --waivers "$WORK/live.waivers"
LINT_ENV=()
if [ "$got" -eq 2 ]; then
  echo '  PASS: gh failing refuses the whole run (exit 2)'
else
  fail "gh failing must exit 2, got $got"; show
fi
printf '%s\n' "$all" | grep -q '^OK:' &&
  { fail 'the OK line printed although the PR listing failed'; show; } ||
  echo '  PASS: no OK line when the PR listing failed'

# The table holds two PRs; a limit of 2 is filled exactly, so the listing may
# have been cut off and must be refused rather than read as complete.
LINT_ENV=(ORPHAN_BRANCH_LINT_LIST_LIMIT=2)
run_lint --waivers "$WORK/live.waivers"
LINT_ENV=()
if [ "$got" -eq 2 ] && printf '%s\n' "$err" | grep -q 'may be truncated'; then
  echo '  PASS: a listing that fills its --limit is refused as possibly truncated (exit 2)'
else
  fail "a listing that fills its --limit must exit 2 as truncated, got $got"; show
fi
printf '%s\n' "$all" | grep -q '^OK:' &&
  { fail 'the OK line printed on a possibly truncated listing'; show; } ||
  echo '  PASS: no OK line on a possibly truncated listing'

LINT_ENV=(PR_EXISTS_STUB_GARBAGE=1)
run_lint --waivers "$WORK/live.waivers"
LINT_ENV=()
[ "$got" -eq 2 ] && echo '  PASS: gh answering non-JSON refuses the run (exit 2)' ||
  { fail "gh answering non-JSON must exit 2, got $got"; show; }

run_lint --waivers "$WORK/does-not-exist.waivers"
[ "$got" -eq 2 ] && echo '  PASS: an unreadable waiver file is refused (exit 2)' ||
  { fail "an unreadable waiver file must exit 2, got $got"; show; }

printf 'feat/old-orphan\n' >"$WORK/noreason.waivers"
run_lint --waivers "$WORK/noreason.waivers"
[ "$got" -eq 2 ] && echo '  PASS: a waiver with no reason is refused (exit 2)' ||
  { fail "a waiver with no reason must exit 2, got $got"; show; }

run_lint --waivers "$WORK/empty.waivers" --max-age-hours soon
[ "$got" -eq 2 ] && echo '  PASS: a non-numeric --max-age-hours is refused (exit 2)' ||
  { fail "a non-numeric --max-age-hours must exit 2, got $got"; show; }

run_lint --waivers "$WORK/empty.waivers" --remote nowhere
[ "$got" -eq 2 ] && echo '  PASS: a remote that cannot be fetched is refused (exit 2)' ||
  { fail "an unfetchable remote must exit 2, got $got"; show; }

# ── 6. The committed waiver file parses ─────────────────────────────────────
# Its entries name real branches on the real origin, so it cannot be run to a
# verdict here; but a malformed line (a waiver with no reason) is refused
# before any fetch, and that refusal is checkable offline.
committed_noreason=$(sed -e 's/#.*//' "$REPO_ROOT/tools/orphan-branch-lint.waivers" |
  awk 'NF == 1 { print }')
[ -z "$committed_noreason" ] && echo '  PASS: every committed waiver carries a reason' ||
  fail "committed waiver(s) with no reason: $committed_noreason"

if [ "$rc" -eq 0 ]; then
  echo 'OK: orphan-branch-lint_test — orphans are named, fresh ones are INFO, stale waivers fail, and a listing it cannot trust refuses the run'
fi
exit "$rc"
