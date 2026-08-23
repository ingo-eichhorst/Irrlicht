#!/usr/bin/env bash
# mutate_test.sh — self-test for tools/mutate.sh (#1750).
#
# tools/mutate.sh is process tooling: it has no "before the fix" runtime
# defect of its own to reproduce, so per docs/testing-philosophy.md its own
# test obligation is to prove each of ITS guards actually triggers its
# failure mode when deliberately violated, and that a normal successful run
# genuinely restores the mutated file to byte-identical content. Every
# assertion below is a guard this change ADDS, so nothing here "passes by
# construction" in the sense AGENTS.md exempts a lock from red-first proof —
# each row is itself the deliberate violation.
#
# Per #1450 (cited directly in #1750): a deliberate no-match row is included
# and MUST be reported as STALE — not pass silently — which is the single
# guard the issue names as "the part that gets forgotten" (#1390).
#
# Convention follows tools/lib/changed-files_test.sh: plain bash, hand-rolled
# asserts, a `fails` counter, "ALL PASS" / "N FAILED" at the end. Run
# directly, or via tools/preflight.sh's `tools` gate.
#
# SAFETY. Every case runs the real tools/mutate.sh as a subprocess against a
# throwaway git repo built fresh per scenario (new_repo, below) — never
# against this repo's own tree. mutate.sh's own preconditions (a file that
# must exist, a tree that must start clean) make that the only sound way to
# drive it: reusing one repo across scenarios would mean cleaning up between
# them, and the cleanup this repo bans everywhere (`git checkout --`/`git
# restore`/`git reset --hard`, since worktrees share the parent's .git dir)
# is exactly what would be tempting here. A fresh `mktemp -d` + `git init`
# per scenario needs none of that.

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
MUTATE_SH="$REPO_ROOT/tools/mutate.sh"

# A missing tool is a hard failure, not a skip — exiting 0 here would read as
# a PASS to preflight's shell_lib_tests, so the gate would go green having
# asserted nothing.
need() { command -v "$1" >/dev/null 2>&1 || { echo "FAIL: mutate_test — $1 not found" >&2; exit 1; }; }
need git
need awk

if [[ ! -x "$MUTATE_SH" ]]; then
  echo "FAIL: mutate_test — $MUTATE_SH is missing or not executable" >&2
  exit 1
fi

fails=0
pass() { echo "  PASS: $1"; return 0; }
fail() { echo "  FAIL: $1 — expected [$2] got [$3]"; fails=$((fails + 1)); return 0; }
assert_eq() {
  if [[ "$2" == "$3" ]]; then pass "$1"; else fail "$1" "$2" "$3"; fi
}
# Every refusal is checked by a FRAGMENT of its message, never just the exit
# code — the same rule tools/lib/await-gone_test.sh follows, so "it exited
# nonzero" cannot be satisfied by the wrong guard firing.
assert_contains() {
  local label="$1" needle="$2" haystack="$3"
  case "$haystack" in
    *"$needle"*) pass "$label" ;;
    *) fail "$label" "output containing: $needle" "${haystack:-nothing was printed}" ;;
  esac
}

# Every scratch repo this suite creates, so the EXIT trap can remove them all
# even if a case fails partway through.
SCRATCH_DIRS=()
cleanup() {
  local d
  for d in "${SCRATCH_DIRS[@]:-}"; do
    [[ -n "$d" && -d "$d" ]] && rm -rf "$d"
  done
}
trap cleanup EXIT

# new_repo — create a fresh git repo with one committed fixture file
# (sample.go, holding an if-block whose braces are a deliberately awkward
# anchor: `[]int{1, 2, 3}` and `arr[0]` below it exercise `[`/`]`, and `* 2`
# exercises `*`, all of which are BASH GLOB METACHARACTERS. mutate.sh matches
# literally through awk's index()/substr(), never through bash's `${var/pat/
# repl}` — which DOES treat `*`/`?`/`[` as wildcards even when the pattern
# variable was assigned with quotes, because that expansion's pattern
# argument is glob-matched regardless. This fixture is what a regression back
# to that shape would actually break on.) Echoes the repo path.
new_repo() {
  local d
  d="$(mktemp -d "${TMPDIR:-/tmp}/mutate-test.XXXXXX")"
  SCRATCH_DIRS+=("$d")
  (
    cd "$d" || exit 1
    git init -q
    git config user.email "test@example.com"
    git config user.name "mutate_test"
    cat > sample.go <<'EOF'
package foo

func Bar() int {
	if x > 0 {
		return 1
	}
	return 0
}

func Baz() int {
	arr := []int{1, 2, 3}
	return arr[0] * 2
}
EOF
    git add sample.go
    git commit -q -m init
  )
  printf '%s' "$d"
}

# run_mutate <repo-dir> <anchor> <replacement> <test-cmd...> — runs mutate.sh
# from inside the repo, capturing combined output in $MUTATE_OUTPUT and the
# exit code in $MUTATE_RC. A subprocess, deliberately: mutate.sh's own `exit`
# calls must not tear down this suite.
MUTATE_OUTPUT=""
MUTATE_RC=0
run_mutate() {
  local repo="$1" anchor="$2" replacement="$3"
  shift 3
  MUTATE_OUTPUT="$(cd "$repo" && "$MUTATE_SH" sample.go "$anchor" "$replacement" "$@" 2>&1)"
  MUTATE_RC=$?
}

THE_IF_ANCHOR=$'\tif x > 0 {\n\t\treturn 1\n\t}'
THE_IF_REPLACED=$'\tif x > 0 {\n\t\treturn 999\n\t}'
GLOBBY_ANCHOR=$'\tarr := []int{1, 2, 3}\n\treturn arr[0] * 2'
GLOBBY_REPLACED=$'\tarr := []int{1, 2, 3}\n\treturn arr[0] * 99'

echo ""
echo "== success: mutation applied, test observes it, file restored byte-for-byte =="
repo="$(new_repo)"
run_mutate "$repo" "$THE_IF_ANCHOR" "$THE_IF_REPLACED" grep -c 'return 999' "$repo/sample.go"
assert_eq "exits 0" "0" "$MUTATE_RC"
assert_contains "banner names the mutated file" "=== mutation applied to sample.go ===" "$MUTATE_OUTPUT"
assert_contains "captures the test command's own output (grep found the mutated line)" $'\n1\n' $'\n'"$MUTATE_OUTPUT"$'\n'
assert_contains "reports the test command's exit status" "exit=0" "$MUTATE_OUTPUT"
assert_contains "reports the tree clean after restore" "git status after restore: ''" "$MUTATE_OUTPUT"
# BYTE-IDENTICAL, not just "git reports no diff": cmp -s compares the actual
# bytes on disk against the committed blob directly, independent of git's own
# diffing (which is what would also read clean if, say, the restore left a
# trailing-newline difference git happens to ignore under its own settings).
if cmp -s <(git -C "$repo" show HEAD:sample.go) "$repo/sample.go"; then
  pass "the file is BYTE-IDENTICAL to before the run"
else
  fail "the file is BYTE-IDENTICAL to before the run" "no diff" "cmp reported a difference"
fi
assert_eq "git status is genuinely empty, not merely unchecked" "" "$(cd "$repo" && git status --porcelain)"

echo ""
echo "== success: a FAILING test command is captured and reported, not swallowed =="
repo="$(new_repo)"
run_mutate "$repo" "$THE_IF_ANCHOR" "$THE_IF_REPLACED" grep -c 'return 1' "$repo/sample.go"
assert_eq "mutate.sh itself still exits 0 (the guards passed; the TEST is what went red)" "0" "$MUTATE_RC"
assert_contains "the red test's own exit status is visible" "exit=1" "$MUTATE_OUTPUT"

echo ""
echo "== literal multi-line matching survives bash glob metacharacters (*, [, ]) =="
repo="$(new_repo)"
run_mutate "$repo" "$GLOBBY_ANCHOR" "$GLOBBY_REPLACED" grep -c '\* 99' "$repo/sample.go"
assert_eq "exits 0 against an anchor containing [ ] and *" "0" "$MUTATE_RC"
assert_contains "the glob-metacharacter mutation actually landed and was observed" $'\n1\n' $'\n'"$MUTATE_OUTPUT"$'\n'

echo ""
echo "== every content read goes through byte_awk(), which is the ONLY place LC_ALL=C is set (static regression pin) =="
# Review finding (#1750): under a UTF-8 locale, GNU awk's length()/index()/
# substr() count Unicode CODEPOINTS, not bytes, so any awk invocation that
# measures or matches file content by byte must run under LC_ALL=C to agree
# with `wc -c`. Centralised in byte_awk() (not repeated per call site) so a
# future content-reading awk call added to this file inherits the fix rather
# than needing to remember it — this pin asserts that centralisation holds:
# exactly ONE `LC_ALL=C ... awk` (byte_awk's own definition), and exactly
# THREE call sites actually going through byte_awk (byte-count, count-guard,
# apply-splice). Deliberately environment-INDEPENDENT (it greps the source,
# it does not run anything) so it catches a regression on every machine
# regardless of which `awk` happens to be on PATH — the live gawk-forced case
# right below this one is the version that can only run where gawk is
# actually installed.
lc_all_c_awk_count=$(grep -v '^[[:space:]]*#' "$MUTATE_SH" | grep -c 'LC_ALL=C.*awk')
assert_eq "LC_ALL=C is set in exactly ONE place (byte_awk's own definition)" "1" "$lc_all_c_awk_count"
byte_awk_call_sites=$(grep -v '^[[:space:]]*#' "$MUTATE_SH" | grep -c "byte_awk '")
assert_eq "exactly 3 call sites go through byte_awk (byte count, count-guard, apply)" "3" "$byte_awk_call_sites"

echo ""
echo "== the same LC_ALL=C fix, exercised live under a real gawk + UTF-8 locale =="
if command -v gawk >/dev/null 2>&1; then
  repo="$(new_repo)"
  # An em dash is 3 bytes in UTF-8 but ONE Unicode codepoint — measured
  # directly (gawk 5.4.1, en_US.UTF-8): length() on a string containing one
  # returns a count 2 SHORTER than `wc -c`, which is exactly what would trip
  # mutate.sh's byte-parity guard (exit 3, "possible NUL byte") on a
  # perfectly valid file if any awk call here were missing its LC_ALL=C.
  printf '// em dash — right here, this repo'"'"'s own prose style\npackage foo\n' > "$repo/prose.go"
  git -C "$repo" add prose.go
  git -C "$repo" commit -q -m "add a file styled like this repo's own docs"
  # A private PATH directory fronts `awk` with a symlink to the real gawk
  # binary, so mutate.sh's own bare `awk` calls resolve to gawk specifically
  # — reproducing the exact interpreter the review finding was about,
  # regardless of which awk happens to be this machine's default.
  gawk_front_dir="$(mktemp -d "${TMPDIR:-/tmp}/mutate-test-gawk.XXXXXX")"
  SCRATCH_DIRS+=("$gawk_front_dir")
  ln -s "$(command -v gawk)" "$gawk_front_dir/awk"
  MUTATE_OUTPUT="$(cd "$repo" && PATH="$gawk_front_dir:$PATH" LC_ALL=en_US.UTF-8 LANG=en_US.UTF-8 \
    "$MUTATE_SH" prose.go "package foo" "package bar" true 2>&1)"
  MUTATE_RC=$?
  assert_eq "succeeds against a multi-byte file under gawk + a UTF-8 locale" "0" "$MUTATE_RC"
  if [[ "$MUTATE_RC" -ne 0 ]]; then
    echo "    (mutate.sh output was: $MUTATE_OUTPUT)"
  fi
else
  echo "  SKIP: gawk not found on PATH — cannot exercise the codepoint-vs-byte bug live on this machine." \
       "The static pin above still catches a regression in the fix; install gawk to also run this case live."
fi

echo ""
echo "== TOCTOU (guard #10): a live file that no longer matches its own snapshot is refused, not silently corrupted =="
# Review finding (#1750): mutate.sh takes ONE snapshot and counts/splices
# against it, then must confirm the LIVE file still matches that snapshot
# before writing back — otherwise a file that changed underneath it (a real
# concurrent writer) could be overwritten with a mutation computed from bytes
# that are no longer there. Reproducing a genuine data race would be a flaky,
# timing-dependent test (this repo's own convention is to poll, never
# sleep-and-hope, for exactly this reason) — so instead this fakes the ONE
# seam that decides this guard: `cmp`, made to always report "different"
# regardless of its actual arguments. Every other command (awk, git, cp)
# still runs for real, so this exercises mutate.sh's real guard, not a stub
# standing in for it.
fake_cmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/mutate-test-fakecmp.XXXXXX")"
SCRATCH_DIRS+=("$fake_cmp_dir")
cat > "$fake_cmp_dir/cmp" <<'EOF'
#!/usr/bin/env bash
exit 1
EOF
chmod +x "$fake_cmp_dir/cmp"

repo="$(new_repo)"
MUTATE_OUTPUT="$(cd "$repo" && PATH="$fake_cmp_dir:$PATH" \
  "$MUTATE_SH" sample.go "$THE_IF_ANCHOR" "$THE_IF_REPLACED" true 2>&1)"
MUTATE_RC=$?
assert_eq "refuses with the dedicated TOCTOU exit code" "10" "$MUTATE_RC"
assert_contains "names the snapshot/live-file discrepancy" "changed after this tool read it" "$MUTATE_OUTPUT"
assert_eq "wrote nothing — the file is untouched" "$(git -C "$repo" show HEAD:sample.go)" "$(cat "$repo/sample.go")"
assert_eq "left the repo clean" "" "$(cd "$repo" && git status --porcelain)"

echo ""
echo "== precondition: outside a git repo reports THAT, not a false 'dirty tree' diagnosis =="
non_repo_dir="$(mktemp -d "${TMPDIR:-/tmp}/mutate-test-norepo.XXXXXX")"
SCRATCH_DIRS+=("$non_repo_dir")
echo "package foo" > "$non_repo_dir/sample.go"
run_mutate "$non_repo_dir" "package foo" "package bar" true
assert_contains "names the real cause (not a git repository), not a dirty-tree misdiagnosis" "not inside a git repository" "$MUTATE_OUTPUT"
[[ "$MUTATE_RC" -ne 0 ]] && pass "refuses (nonzero exit)" || fail "refuses (nonzero exit)" "nonzero" "0"

echo ""
echo "== STALE: a deliberate no-match row must be reported as STALE, never pass silently (#1390, #1450) =="
repo="$(new_repo)"
run_mutate "$repo" "this text does not occur anywhere in sample.go" "replacement" true
[[ "$MUTATE_RC" -ne 0 ]] && pass "refuses (nonzero exit)" || fail "refuses (nonzero exit)" "nonzero" "0"
assert_contains "the refusal literally says STALE" "STALE" "$MUTATE_OUTPUT"
assert_contains "...and names the guard it is enforcing" "occurs 0 times" "$MUTATE_OUTPUT"
assert_eq "left the repo clean — a refused run must not leave partial state" "" "$(cd "$repo" && git status --porcelain)"
assert_eq "left the file byte-for-byte untouched" "$(cat "$repo/sample.go")" "$(cd "$repo" && git show HEAD:sample.go)"

echo ""
echo "== ambiguous: an anchor matching more than once is refused, not applied to the first hit =="
repo="$(new_repo)"
run_mutate "$repo" "return" "come back" true
assert_contains "names the actual count found (return 1 / return 0 / return arr[0] * 2)" "occurs 3 times" "$MUTATE_OUTPUT"
assert_eq "left the repo clean" "" "$(cd "$repo" && git status --porcelain)"

echo ""
echo "== refuses a no-op replacement (byte-identical to the anchor) before touching the file =="
repo="$(new_repo)"
run_mutate "$repo" "$THE_IF_ANCHOR" "$THE_IF_ANCHOR" true
assert_contains "names it as a no-op / unchanged" "UNCHANGED" "$MUTATE_OUTPUT"
assert_eq "left the repo clean" "" "$(cd "$repo" && git status --porcelain)"

echo ""
echo "== refuses an empty anchor =="
repo="$(new_repo)"
run_mutate "$repo" "" "something" true
assert_contains "names the empty anchor" "anchor is empty" "$MUTATE_OUTPUT"

echo ""
echo "== refuses a missing file rather than creating or silently skipping one =="
repo="$(new_repo)"
MUTATE_OUTPUT="$(cd "$repo" && "$MUTATE_SH" no-such-file.go "$THE_IF_ANCHOR" "$THE_IF_REPLACED" true 2>&1)"
MUTATE_RC=$?
assert_contains "names the missing file" "does not exist" "$MUTATE_OUTPUT"
[[ "$MUTATE_RC" -ne 0 ]] && pass "refuses (nonzero exit)" || fail "refuses (nonzero exit)" "nonzero" "0"

echo ""
echo "== precondition: refuses to start against an ALREADY-dirty tree =="
# The postcondition (git status --porcelain empty AFTER restoring) can only
# mean anything if the tree was clean BEFORE mutate.sh touched it — otherwise
# a genuine restore failure could hide behind pre-existing mess.
repo="$(new_repo)"
echo "uncommitted change" >> "$repo/sample.go"
run_mutate "$repo" "$THE_IF_ANCHOR" "$THE_IF_REPLACED" true
assert_contains "refuses, naming the precondition" "already non-empty BEFORE mutating" "$MUTATE_OUTPUT"
git -C "$repo" checkout -q -- sample.go   # test-harness cleanup of ITS OWN scratch repo, not the tool under test

echo ""
echo "== refuses a file it cannot read byte-for-byte (embedded NUL) rather than risk a corrupt read =="
repo="$(new_repo)"
printf 'abc\x00def\n' > "$repo/embedded-nul.bin"
git -C "$repo" add embedded-nul.bin
git -C "$repo" commit -q -m "add a file with an embedded NUL"
MUTATE_OUTPUT="$(cd "$repo" && "$MUTATE_SH" embedded-nul.bin "abc" "xyz" true 2>&1)"
MUTATE_RC=$?
assert_contains "names the byte-count mismatch" "could not read" "$MUTATE_OUTPUT"
[[ "$MUTATE_RC" -ne 0 ]] && pass "refuses (nonzero exit)" || fail "refuses (nonzero exit)" "nonzero" "0"

echo ""
echo "== usage: refuses when the test command is missing entirely =="
repo="$(new_repo)"
run_mutate "$repo" "$THE_IF_ANCHOR" "$THE_IF_REPLACED"   # no trailing test-cmd args at all
assert_contains "prints a usage line" "usage:" "$MUTATE_OUTPUT"
[[ "$MUTATE_RC" -ne 0 ]] && pass "refuses (nonzero exit)" || fail "refuses (nonzero exit)" "nonzero" "0"

echo ""
echo "== FATAL: a test command's own side effect leaves the tree dirty — reported LOUDLY, not silently =="
# mutate.sh restores the FILE it mutated; it cannot undo something the test
# command itself did to the rest of the tree. That must surface as a hard,
# named failure — never as a quiet 'git status after restore' that nobody
# reads. This is guard #9's own deliberate violation.
repo="$(new_repo)"
run_mutate "$repo" "$THE_IF_ANCHOR" "$THE_IF_REPLACED" bash -c 'touch side-effect.txt'
assert_contains "names it FATAL" "FATAL" "$MUTATE_OUTPUT"
assert_contains "names the leftover file" "side-effect.txt" "$MUTATE_OUTPUT"
[[ "$MUTATE_RC" -ne 0 ]] && pass "refuses (nonzero exit)" || fail "refuses (nonzero exit)" "nonzero" "0"
# The mutated FILE itself must still have been restored even though the
# overall tree is reported dirty — the guard's whole point is telling those
# two facts apart rather than conflating them.
assert_eq "the file mutate.sh actually touched IS still restored" \
  "$(git -C "$repo" show HEAD:sample.go)" "$(cat "$repo/sample.go")"
rm -f "$repo/side-effect.txt"   # test-harness cleanup, not the tool under test

echo ""
if [[ "$fails" -eq 0 ]]; then
  echo "mutate_test: ALL PASS"
  exit 0
fi
echo "mutate_test: $fails FAILED"
exit 1
