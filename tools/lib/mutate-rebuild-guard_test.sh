#!/usr/bin/env bash
# mutate-rebuild-guard_test.sh — mutate.sh must not restore a source file with
# a PRE-mutation mtime.
#
# WHY THIS FILE EXISTS (#1852). `restore_file` used `cp -p "$BACKUP" "$FILE"`.
# `-p` preserves the backup's timestamps, so a byte-perfect restore stamped the
# source as OLDER than any build product compiled from the mutated source while
# the mutation was applied. Every mtime-driven build system then treats that
# product as up to date and refuses to rebuild it — so the mutated binary
# outlives the mutation, while the source, the test suite and
# `git status --porcelain` are all clean and correct. Nothing in the harness's
# own post-conditions can see it: they all check CONTENT.
#
# The incident: during #1852 a `swift build` under an applied mutation left
# `platforms/macos/.build/debug/Irrlicht` carrying the mutated
# `MenuBarAppearance.usesNarrowQuotaBars`. It was installed 22 seconds after
# the restore and reported as a shipped product defect. Disassembling the
# installed binary against a fresh build of the same commit differed by exactly
# one instruction out of 1,003,608 — the mutated `switch` arm. Correct code was
# nearly "fixed" on the strength of it.
#
# This is a guard the change ADDS, so it has no "before the fix" to run red
# against — the mutation is `touch "$FILE"` -> removed, which is exactly the
# pre-#1852 code (AGENTS.md, Testing).
#
# Convention follows tools/lib/mutate_test.sh: plain bash, hand-rolled asserts,
# a `fails` counter, "ALL PASS" / "N FAILED" at the end.

set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
MUTATE="$REPO_ROOT/tools/mutate.sh"
fails=0

fail() { echo "  FAIL: $*"; fails=$((fails + 1)); }
pass() { echo "  ok: $*"; }

if [[ ! -x "$MUTATE" ]]; then
  echo "COULD NOT LOOK — $MUTATE is missing or not executable" >&2
  exit 1
fi

# A throwaway git repo, so mutate.sh's own clean-tree preconditions hold and
# nothing touches the real worktree.
WORK="$(mktemp -d "${TMPDIR:-/tmp}/mutate-rebuild.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT
cd "$WORK" || exit 1
git init -q .
git config user.email t@example.com
git config user.name t
printf 'alpha\nbravo\n' > subject.txt
# The build product must be ignored: mutate.sh refuses (exit 9) if the tree is
# dirty after restoring, and an untracked artifact would look like a failed
# restore rather than like the thing the mutation was supposed to produce.
printf 'product.bin\n' > .gitignore
git add -A
git commit -qm init

# Backdate the source well past any filesystem timestamp granularity, so
# "unchanged" and "bumped" cannot be confused by a coarse clock.
touch -t 200109090146.40 subject.txt
before=$(stat -f %m subject.txt 2>/dev/null || stat -c %Y subject.txt)

# Only that it landed far in the past matters, not the exact epoch: `touch -t`
# reads local time, so pinning a literal epoch would make this fail by timezone
# rather than by defect. A year is many orders of magnitude past any filesystem
# timestamp granularity, which is all the margin this needs.
now_epoch=$(date +%s)
if [[ -z "$before" || $((now_epoch - before)) -lt 31536000 ]]; then
  echo "COULD NOT LOOK — backdating subject.txt did not take (mtime=$before, now=$now_epoch)" >&2
  exit 1
fi

# A build product compiled WHILE the mutation is applied: newer than the
# backdated source, which is the whole hazard.
"$MUTATE" subject.txt 'bravo' 'BRAVO' \
  sh -c 'cp subject.txt product.bin' >/dev/null 2>&1
mutate_rc=$?

if [[ ! -f product.bin ]]; then
  echo "COULD NOT LOOK — the test command never ran, so nothing was proved" >&2
  exit 1
fi
if [[ "$mutate_rc" -ne 0 ]]; then
  echo "COULD NOT LOOK — mutate.sh exited $mutate_rc on a mutation expected to succeed" >&2
  exit 1
fi

after=$(stat -f %m subject.txt 2>/dev/null || stat -c %Y subject.txt)

# 1. The product really was built from the MUTATED source — otherwise this
#    test would pass for the wrong reason.
if grep -q 'BRAVO' product.bin; then
  pass "the build product captured the mutated source"
else
  fail "product.bin does not contain the mutation — the hazard was not reproduced"
fi

# 2. Content is restored byte-for-byte (mutate.sh's existing promise).
if [[ "$(cat subject.txt)" == "$(printf 'alpha\nbravo\n')" ]]; then
  pass "source content restored byte-for-byte"
else
  fail "source content was NOT restored"
fi

# 3. THE GUARD. The restored source must be NEWER than the product built while
#    the mutation was applied, so an mtime-driven build rebuilds it away.
product_mtime=$(stat -f %m product.bin 2>/dev/null || stat -c %Y product.bin)
if [[ "$after" -gt "$before" ]]; then
  pass "restore bumped the source mtime ($before -> $after)"
else
  fail "restore left the PRE-mutation mtime ($after): a build product compiled from the mutated source stays 'up to date' and is never rebuilt"
fi
if [[ "$after" -ge "$product_mtime" ]]; then
  pass "restored source is not older than the contaminated product"
else
  fail "restored source ($after) is OLDER than the product built under mutation ($product_mtime) — the stale binary survives the next build"
fi

if [[ "$fails" -eq 0 ]]; then
  echo "ALL PASS"
  exit 0
fi
echo "$fails FAILED"
exit 1
