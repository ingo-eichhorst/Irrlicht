#!/usr/bin/env bash
# git-hooks_test.sh — unit tests for tools/install-git-hooks.sh and
# tools/git-hooks/shim. Plain bash (no framework), matching the style of the
# other tools/lib/*_test.sh files. Run directly, or via tools/preflight.sh's
# `tools` gate. Exits non-zero on any failed assertion.
#
# Covers issue #1591: linked worktrees share the parent repo's .git/hooks/, and
# the installer used to put a SYMLINK to the main checkout's script there — so
# a hook change made in a worktree did not govern that worktree's own push, and
# anything under tools/git-hooks/ was untestable from the branch that changed
# it. The headline assertion is `worktree's own refusing hook is the one that
# runs`, and the mutation that makes it mean something is committed beside it
# rather than described in a PR body: `pre-#1591 symlink install` builds the
# identical rig, installs the old way, and pins the OPPOSITE outcome. Same
# worktree, same refusing hook, one difference — the install method — and the
# push is allowed. Without that case, an assertion that a refusal happened
# could be satisfied by a rig where nothing worked at all.
#
# NOTHING HERE TOUCHES THE REAL .git/hooks/. Every case builds a throwaway
# repo trio (bare "origin" + main checkout + linked worktree) under mktemp -d,
# and pushes over a filesystem path. The hooks installed are stubs that echo a
# marker; tools/preflight.sh is never invoked.

set -uo pipefail   # NOT -e: assertions capture non-zero return codes

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"

# Hermetic git: a developer (or CI image) carrying a global core.hooksPath, or
# a commit.gpgsign, would otherwise change what these cases measure. Pinning
# both config layers to /dev/null is why every rig sets user.name/email itself.
export GIT_CONFIG_GLOBAL=/dev/null
export GIT_CONFIG_SYSTEM=/dev/null
export GIT_TERMINAL_PROMPT=0

fails=0
pass() {
  local label="$1"
  echo "  PASS: $label"
  return 0
}
fail() {
  local label="$1" expected="$2" got="$3"
  echo "  FAIL: $label — expected [$expected] got [$got]"
  fails=$((fails + 1))
  return 0
}
assert_eq() {
  local label="$1" expected="$2" actual="$3"
  [[ "$expected" == "$actual" ]] && pass "$label" || fail "$label" "$expected" "$actual"
  return 0
}
assert_contains() {
  local label="$1" needle="$2" haystack="$3"
  case "$haystack" in
    *"$needle"*) pass "$label" ;;
    *) fail "$label" "contains: $needle" "$haystack" ;;
  esac
  return 0
}
assert_not_contains() {
  local label="$1" needle="$2" haystack="$3"
  case "$haystack" in
    *"$needle"*) fail "$label" "does NOT contain: $needle" "$haystack" ;;
    *) pass "$label" ;;
  esac
  return 0
}

# One parent temp dir, one trap. Rigs are subdirectories of it rather than
# separate mktemp -d results collected into an array: `RIG=$(new_rig)` runs in
# a command substitution, so anything the helper appended to a shell array
# there would be discarded with the subshell and the rigs would outlive the
# run.
ROOT_TMP="$(mktemp -d)"
trap 'rm -rf "$ROOT_TMP"' EXIT

# hook_stub <marker> <exit-code> — a pre-push hook that identifies WHICH COPY
# of the script ran and then succeeds or refuses. It reports the marker baked
# into the script text, never anything read from the cwd: git runs hooks with
# cwd at the pushing worktree either way, so a cwd-derived marker would say
# "worktree" even when the main checkout's script is the one executing — which
# is precisely the confusion this whole issue is about.
hook_stub() {
  cat <<EOF
#!/bin/sh
echo "HOOK-SCRIPT-FROM=$1"
echo "HOOK-ARGC=\$# HOOK-ARG1=\${1-none}"
read -r first_ref_line
[ -n "\$first_ref_line" ] && echo "HOOK-STDIN=nonempty"
exit $2
EOF
}

# make_rig <dir> — bare origin + main checkout (carrying this repo's real
# installer and shim, plus a pre-push stub marked `main`) + a linked worktree
# on branch `feature`. Leaves NO hook installed; each case chooses how.
make_rig() {
  local root="$1"
  mkdir -p "$root"
  (
    set -e
    cd "$root"
    git init -q --bare origin.git
    git init -q -b main checkout
    cd checkout
    git config user.email t@t
    git config user.name t
    git remote add origin "$root/origin.git"
    mkdir -p tools/git-hooks
    cp "$REPO_ROOT/tools/install-git-hooks.sh" tools/install-git-hooks.sh
    cp "$REPO_ROOT/tools/git-hooks/shim" tools/git-hooks/shim
    chmod +x tools/install-git-hooks.sh tools/git-hooks/shim
    hook_stub main 0 >tools/git-hooks/pre-push
    chmod +x tools/git-hooks/pre-push
    git add -A
    git commit -qm base
    git push -q origin main
    git worktree add -q ../worktree -b feature
    cd ../worktree
    git config user.email t@t
    git config user.name t
  ) >/dev/null 2>&1
}

new_rig() {
  local t
  t="$(mktemp -d "$ROOT_TMP/rig.XXXXXX")"
  make_rig "$t"
  echo "$t"
}

# ===========================================================================
echo "== the installer writes a shim, not a symlink, and not the hook itself =="
RIG="$(new_rig)"
(cd "$RIG/checkout" && bash tools/install-git-hooks.sh) >/dev/null 2>&1
TARGET="$RIG/checkout/.git/hooks/pre-push"
if [[ -L "$TARGET" ]]; then kind=symlink; elif [[ -f "$TARGET" ]]; then kind=file; else kind=absent; fi
assert_eq "installed pre-push is a regular file" "file" "$kind"
if cmp -s "$REPO_ROOT/tools/git-hooks/shim" "$TARGET"; then same=yes; else same=no; fi
assert_eq "installed pre-push is byte-for-byte tools/git-hooks/shim" "yes" "$same"
if [[ -x "$TARGET" ]]; then x=yes; else x=no; fi
assert_eq "installed pre-push is executable" "yes" "$x"

# ===========================================================================
echo "== #1591 HEADLINE: a worktree's own hook governs that worktree's push =="
# The worktree's copy of the hook refuses; the main checkout's still passes.
# With the shim installed, the refusal must be the WORKTREE's.
RIG="$(new_rig)"
(cd "$RIG/checkout" && bash tools/install-git-hooks.sh) >/dev/null 2>&1
(
  cd "$RIG/worktree"
  hook_stub worktree 1 >tools/git-hooks/pre-push
  chmod +x tools/git-hooks/pre-push
  git add -A && git commit -qm "worktree hook refuses"
) >/dev/null 2>&1
out=$(cd "$RIG/worktree" && git push origin feature 2>&1)
rc=$?
assert_eq "push is REFUSED" "1" "$rc"
assert_contains "the hook that ran is the WORKTREE's copy" "HOOK-SCRIPT-FROM=worktree" "$out"
assert_not_contains "the main checkout's copy did NOT run" "HOOK-SCRIPT-FROM=main" "$out"
# exec must not eat the hook's interface: git hands a pre-push hook the remote
# name and URL as argv, and the ref lines on stdin.
assert_contains "argv survives the shim's exec" "HOOK-ARGC=2 HOOK-ARG1=origin" "$out"
assert_contains "stdin survives the shim's exec" "HOOK-STDIN=nonempty" "$out"

# ===========================================================================
echo "== the committed mutation: the pre-#1591 symlink install lets it through =="
# Identical rig, identical refusing worktree hook. The ONLY difference is the
# install method. If this case ever starts refusing too, the headline above has
# stopped discriminating and is passing for some other reason.
RIG="$(new_rig)"
ln -sf "$RIG/checkout/tools/git-hooks/pre-push" "$RIG/checkout/.git/hooks/pre-push"
(
  cd "$RIG/worktree"
  hook_stub worktree 1 >tools/git-hooks/pre-push
  chmod +x tools/git-hooks/pre-push
  git add -A && git commit -qm "worktree hook refuses"
) >/dev/null 2>&1
out=$(cd "$RIG/worktree" && git push origin feature 2>&1)
rc=$?
assert_eq "old symlink install: the refusing push is ALLOWED — the defect" "0" "$rc"
assert_contains "old symlink install: the MAIN checkout's copy ran" "HOOK-SCRIPT-FROM=main" "$out"
assert_not_contains "old symlink install: the worktree's copy never ran" "HOOK-SCRIPT-FROM=worktree" "$out"

# ===========================================================================
echo "== a revision that genuinely carries no hook passes DELIBERATELY, loudly =="
# The bisect / old-branch case: refusing here would punish `git bisect` for a
# hook that was never this revision's to run.
RIG="$(new_rig)"
(cd "$RIG/checkout" && bash tools/install-git-hooks.sh) >/dev/null 2>&1
(
  cd "$RIG/worktree"
  git rm -q tools/git-hooks/pre-push
  git commit -qm "a revision from before the hook existed"
) >/dev/null 2>&1
out=$(cd "$RIG/worktree" && git push origin feature 2>&1)
rc=$?
assert_eq "no hook in HEAD and none on disk → push ALLOWED" "0" "$rc"
assert_contains "…and it says so rather than passing in silence" "carries no tools/git-hooks/pre-push" "$out"

# ===========================================================================
echo "== a hook missing from the tree but present in HEAD REFUSES, loudly =="
# A deleted file / interrupted checkout is an accident, not a revision without
# the gate. A gate skipped because a file was invisible is this repo's
# most-repeated failure shape.
RIG="$(new_rig)"
(cd "$RIG/checkout" && bash tools/install-git-hooks.sh) >/dev/null 2>&1
(cd "$RIG/worktree" && echo x >x && git add x && git commit -qm work) >/dev/null 2>&1
rm -f "$RIG/worktree/tools/git-hooks/pre-push"
out=$(cd "$RIG/worktree" && git push origin feature 2>&1)
rc=$?
assert_eq "HEAD carries the hook, disk does not → push REFUSED" "1" "$rc"
assert_contains "…and it names the broken working tree" "broken working tree" "$out"

# ===========================================================================
echo "== a present-but-not-executable hook REFUSES too =="
RIG="$(new_rig)"
(cd "$RIG/checkout" && bash tools/install-git-hooks.sh) >/dev/null 2>&1
(cd "$RIG/worktree" && echo x >x && git add x && git commit -qm work) >/dev/null 2>&1
chmod -x "$RIG/worktree/tools/git-hooks/pre-push"
out=$(cd "$RIG/worktree" && git push origin feature 2>&1)
rc=$?
assert_eq "hook present but chmod -x → push REFUSED" "1" "$rc"
assert_contains "…and it says what to do about it" "chmod +x" "$out"

# ===========================================================================
echo "== the installer replaces an older non-shim hook, and is idempotent =="
RIG="$(new_rig)"
TARGET="$RIG/checkout/.git/hooks/pre-push"
# Start from exactly what the pre-#1591 installer left behind.
ln -sf "$RIG/checkout/tools/git-hooks/pre-push" "$TARGET"
first=$(cd "$RIG/checkout" && bash tools/install-git-hooks.sh 2>&1)
assert_contains "an older symlink install is replaced, not left in place" "installed   pre-push" "$first"
if [[ -L "$TARGET" ]]; then kind=symlink; elif [[ -f "$TARGET" ]]; then kind=file; else kind=absent; fi
assert_eq "…by a regular file" "file" "$kind"
# The `rm -f` before `cp` is what makes this safe: `cp` onto a symlink writes
# THROUGH it, so without the rm this run would have overwritten the tracked
# hook script with the shim's bytes.
assert_contains "the tracked hook script was NOT written through the symlink" \
  "HOOK-SCRIPT-FROM=main" "$(cat "$RIG/checkout/tools/git-hooks/pre-push")"
second=$(cd "$RIG/checkout" && bash tools/install-git-hooks.sh 2>&1)
assert_contains "a second run installs nothing" "up to date  pre-push" "$second"
assert_not_contains "…and does not reinstall" "installed   pre-push" "$second"
if [[ -e "$RIG/checkout/.git/hooks/shim" ]]; then stray=yes; else stray=no; fi
assert_eq "the shim itself is never installed as a hook" "no" "$stray"

# ===========================================================================
echo "== the installer works from any cwd, and from a linked worktree =="
# `git rev-parse --git-common-dir` answers relative to the CALLER's cwd, so an
# installer that used it raw would create ./.git/hooks wherever it was invoked
# and install nothing where git looks.
RIG="$(new_rig)"
ELSEWHERE="$(mktemp -d "$ROOT_TMP/elsewhere.XXXXXX")"
(cd "$ELSEWHERE" && bash "$RIG/checkout/tools/install-git-hooks.sh") >/dev/null 2>&1
if cmp -s "$REPO_ROOT/tools/git-hooks/shim" "$RIG/checkout/.git/hooks/pre-push"; then same=yes; else same=no; fi
assert_eq "run from an unrelated cwd → the hook still lands in the real hooks dir" "yes" "$same"
if [[ -e "$ELSEWHERE/.git" ]]; then stray=yes; else stray=no; fi
assert_eq "…and no stray .git is created in that cwd" "no" "$stray"

RIG="$(new_rig)"
# From the worktree, --git-common-dir must resolve to the MAIN checkout's .git.
(cd "$RIG/worktree" && bash tools/install-git-hooks.sh) >/dev/null 2>&1
if cmp -s "$REPO_ROOT/tools/git-hooks/shim" "$RIG/checkout/.git/hooks/pre-push"; then same=yes; else same=no; fi
assert_eq "run from a linked worktree → installs into the SHARED hooks dir" "yes" "$same"
if [[ -e "$RIG/worktree/.git/hooks/pre-push" ]]; then stray=yes; else stray=no; fi
assert_eq "…not into a per-worktree hooks dir git never reads" "no" "$stray"

# ===========================================================================
echo "== the installer refuses when the shim is missing =="
RIG="$(new_rig)"
rm -f "$RIG/checkout/tools/git-hooks/shim"
out=$(cd "$RIG/checkout" && bash tools/install-git-hooks.sh 2>&1)
rc=$?
assert_eq "no shim → exit 1, not a hook chain with nothing to delegate through" "1" "$rc"
assert_contains "…and it names the file" "tools/git-hooks/shim" "$out"

echo ""
if [[ "$fails" -eq 0 ]]; then
  echo "git-hooks_test: ALL PASS"
else
  echo "git-hooks_test: $fails FAILED" >&2
  exit 1
fi
