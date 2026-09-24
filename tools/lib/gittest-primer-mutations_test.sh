#!/usr/bin/env bash
# gittest-primer-mutations_test.sh — committed mutation fixture for #2047's
# git primer (core/adapters/outbound/git/gittest).
#
# The primer is an ADDED guard, so it has no "before the fix" to run red; this
# mutates what it protects instead. Pointing gittest's resolved binary at a
# path that does not exist must make every package whose TestMain primes git
# fail before a single test runs, naming the binary — a primer that cannot run
# must fail loudly, never skip. The four packages are the ones that run the real
# git adapter (`grep -rln 'outbound/git"' core --include='*_test.go'`, plus the
# git package itself). A package whose TestMain lost the call would stay green
# here and be named.
#
# The red half of #2047 (the cold xcrun cache itself) is a separate, opt-in
# fixture: core/adapters/outbound/git/gittest/coldcache_repro_darwin_test.go.
#
# tools/mutate.sh owns the mechanics this file must not re-improvise.

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
MUTATE_SH="$REPO_ROOT/tools/mutate.sh"
NAME="gittest-primer-mutations"

for tool in go git; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "FAIL: $NAME — $tool not found" >&2
    exit 1
  fi
done
if [[ ! -x "$MUTATE_SH" ]]; then
  echo "FAIL: $NAME — $MUTATE_SH is missing or not executable" >&2
  exit 1
fi

if [[ -n "$(git -C "$REPO_ROOT" status --porcelain)" ]]; then
  echo "$NAME: CANNOT RUN — the worktree is dirty, and mutate.sh needs a clean tree" >&2
  echo "  for its post-restore check to mean anything. Commit or clean up, then:" >&2
  echo "    bash tools/lib/$NAME""_test.sh" >&2
  if [[ -n "${CI:-}" || -n "${MUTATION_FIXTURES_STRICT:-}" ]]; then
    echo "  (failing rather than skipping: CI/strict mode)" >&2
    exit 1
  fi
  echo "  (skipped locally; set MUTATION_FIXTURES_STRICT=1 to make this a failure)" >&2
  exit 0
fi

PKGS=(
  irrlicht/core/adapters/outbound/git
  irrlicht/core/adapters/outbound/filesystem
  irrlicht/core/application/services
  irrlicht/core/cmd/irrlichd
)
BOGUS="/nonexistent/irrlicht-2047-git"
FILE="core/adapters/outbound/git/gittest/prime.go"
ANCHOR='var binary = pathutil.MustResolve("git")'
REPLACEMENT="var binary = pathutil.MustResolve(\"git\")[:0] + \"$BOGUS\""

# -run '^$' runs no test, but TestMain (and so the primer) still runs first.
out="$(cd "$REPO_ROOT" && "$MUTATE_SH" "$FILE" "$ANCHOR" "$REPLACEMENT" \
  bash -c "cd core && go test -count=1 -run '^\$' ${PKGS[*]} 2>&1; echo GO_TEST_RC=\$?" 2>&1)"
rc=$?

fails=0
fail() { echo "FAIL: $NAME — $1"; echo "$out" | sed 's/^/      | /'; fails=$((fails + 1)); }

if [[ $rc -ne 0 ]]; then
  fail "mutate.sh refused (exit $rc). A STALE or ambiguous anchor means prime.go moved and this fixture needs updating — it does NOT mean the primer is fine."
elif grep -q 'GO_TEST_RC=0' <<<"$out"; then
  fail "every package stayed GREEN with the primer pointed at a missing binary."
elif grep -qE 'build failed|cannot use|undefined:' <<<"$out"; then
  fail "the mutation broke the BUILD rather than the primer."
else
  for pkg in "${PKGS[@]}"; do
    if ! grep -qE "^FAIL[[:space:]]+$pkg([[:space:]]|$)" <<<"$out"; then
      fail "$pkg did not fail — its TestMain does not prime git."
    fi
  done
  if ! grep -qF "gittest: $BOGUS --version failed after" <<<"$out"; then
    fail "no package reported the primer failure by name."
  fi
fi

if [[ $fails -gt 0 ]]; then
  echo "$NAME: $fails FAILED"
  exit 1
fi
echo "ok  pointing the primer at a missing binary fails all ${#PKGS[@]} primed packages"
echo "$NAME: ALL PASS"
