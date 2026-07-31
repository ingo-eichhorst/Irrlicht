#!/usr/bin/env bash
# changed-files_test.sh — unit tests for lib/changed-files.sh. Plain bash (no
# framework), matching the style of tools/onboarding-factory/scripts/lib/*_test.sh.
# Run directly, or via tools/preflight.sh's `tools` gate. Exits non-zero on any
# failed assertion.
#
# Covers issue #1213: tools/security-scan.sh audited both web trees (and every
# Go module) unconditionally, so a Go-only push was rejected by a pre-existing
# npm advisory it could not have caused. These tests pin the scoping predicates
# that decide what a given diff actually feeds — including the two regressions
# that matter:
#   1. a Go-only diff puts NO web tree in scope (the reported failure);
#   2. a lockfile-only diff inside tools/onboarding-factory/internal/viewer/web
#      does NOT drag in the enclosing tools/onboarding-factory Go module.

set -uo pipefail   # NOT -e: assertions capture non-zero return codes

DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=changed-files.sh
source "$DIR/changed-files.sh"

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
# assert_scope <label> <in|out> <predicate> <path> <changed-set>
assert_scope() {
  local label="$1" want="$2" pred="$3" path="$4" set="$5" got
  if "$pred" "$path" "$set"; then got="in"; else got="out"; fi
  assert_eq "$label" "$want" "$got"
  return 0
}

# The real module/tree lists from tools/security-scan.sh, so a rename there
# that these tests don't follow shows up as a failure rather than silence.
GO_MODULES=(core tools/onboarding-factory tools/wsload tools/seed-demo-sessions tools/eta-research tools/starhistory)
WEB_TREES=(platforms/web tools/onboarding-factory/internal/viewer/web)

# PR #1207's shape: a Go + bash change, zero files under either web tree.
# The paths are illustrative — these predicates are pure string matching and
# never stat the filesystem — so nothing here breaks if a real file is renamed.
GO_ONLY_DIFF=$(printf '%s\n' \
  core/domain/session/session.go \
  core/application/services/session_service.go \
  tools/preflight.sh \
  AGENTS.md)

# The mirror case: an npm-only diff, e.g. `npm audit fix` in the viewer tree.
NPM_ONLY_DIFF="tools/onboarding-factory/internal/viewer/web/package-lock.json"

echo "== go_module_touched =="
assert_scope "core Go source puts core in scope" \
  in go_module_touched core "$GO_ONLY_DIFF"
assert_scope "core/go.mod puts core in scope" \
  in go_module_touched core "core/go.mod"
assert_scope "core/go.sum puts core in scope" \
  in go_module_touched core "core/go.sum"
assert_scope "a Go source deep under a module puts it in scope" \
  in go_module_touched tools/onboarding-factory "tools/onboarding-factory/internal/viewer/viewer.go"
assert_scope "an unrelated module stays out of scope for a core-only diff" \
  out go_module_touched tools/starhistory "$GO_ONLY_DIFF"
assert_scope "a non-Go file under a module does not put it in scope" \
  out go_module_touched core "core/bin/irrlichd"
assert_scope "a bare tools/*.sh change puts no Go module in scope" \
  out go_module_touched core "tools/preflight.sh"
assert_scope "a path merely prefixed by the module name is not inside it" \
  out go_module_touched tools/wsload "tools/wsloader/main.go"

echo "== go_module_touched: the nested web tree must not pull in its Go module =="
assert_scope "#1213 mirror: viewer lockfile does NOT scope in tools/onboarding-factory" \
  out go_module_touched tools/onboarding-factory "$NPM_ONLY_DIFF"
assert_scope "#1213 mirror: viewer lockfile scopes in no Go module at all" \
  out go_module_touched core "$NPM_ONLY_DIFF"

echo "== web_tree_touched =="
assert_scope "a lockfile change puts its own tree in scope" \
  in web_tree_touched tools/onboarding-factory/internal/viewer/web "$NPM_ONLY_DIFF"
assert_scope "a package.json change puts its tree in scope" \
  in web_tree_touched platforms/web "platforms/web/package.json"
assert_scope "the other web tree stays out of scope" \
  out web_tree_touched platforms/web "$NPM_ONLY_DIFF"
assert_scope "a .js change alone does not put a tree in scope (npm audit reads neither)" \
  out web_tree_touched platforms/web "platforms/web/irrlicht.js"
assert_scope "a nested package.json under node_modules is not the tree's manifest" \
  out web_tree_touched platforms/web "platforms/web/node_modules/postcss/package.json"

echo "== #1213 regression: a Go-only push scopes in no web tree =="
for tree in "${WEB_TREES[@]}"; do
  assert_scope "Go-only diff → npm audit SKIPS $tree" \
    out web_tree_touched "$tree" "$GO_ONLY_DIFF"
done

echo "== an empty changed set scopes everything out =="
for mod in "${GO_MODULES[@]}"; do
  assert_scope "empty diff → govulncheck/gosec SKIP $mod" \
    out go_module_touched "$mod" ""
done
for tree in "${WEB_TREES[@]}"; do
  assert_scope "empty diff → npm audit SKIPS $tree" \
    out web_tree_touched "$tree" ""
done

echo "== changed_files_vs_origin_main: real repo, real git =="
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
(
  cd "$TMP" || exit 1
  git init -q . && git config user.email t@t && git config user.name t
  mkdir -p core && echo package main >core/main.go
  git add -A && git commit -qm base
  # Stand in for the remote: origin/main == the base commit.
  git update-ref refs/remotes/origin/main HEAD
  echo "// committed" >>core/main.go && git commit -qam work
  echo '{}' >core/go.mod && git add core/go.mod      # staged, uncommitted
  echo unstaged >README.md                            # untracked-but-modified peer
  git add README.md && echo more >>README.md          # staged AND then modified
) >/dev/null 2>&1
out=$( cd "$TMP" && . "$DIR/changed-files.sh" && changed_files_vs_origin_main )
assert_eq "collects committed + staged + unstaged, sorted and de-duplicated" \
  "$(printf 'README.md\ncore/go.mod\ncore/main.go')" "$out"

echo ""
if [[ "$fails" -eq 0 ]]; then
  echo "changed-files_test: ALL PASS"
else
  echo "changed-files_test: $fails FAILED" >&2
  exit 1
fi
