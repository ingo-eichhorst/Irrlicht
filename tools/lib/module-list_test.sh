#!/usr/bin/env bash
# module-list_test.sh — hold .github/dependabot.yml's directory lists against
# the repo's real inventory of Go modules and web trees (#1291).
#
# Why this exists rather than deriving, as tools/security-scan.sh now does:
# dependabot.yml is static YAML read by GitHub's infrastructure, not by us, so
# there is nothing to derive at. It can only be asserted. Left unasserted, a
# seventh go.work module means that module is never dependency-updated — and
# nothing surfaces that, because the repo builds and tests exactly as before.
#
# This is a lock, not a defect test: it passes by construction against a
# correct repo. Its whole value is that it *can* fail, so both assertions were
# seen red against a deliberately mutated dependabot.yml before landing.
set -uo pipefail

REPO_ROOT=$(git rev-parse --show-toplevel)
# Every path below is repo-relative, so a failed cd would compare whatever
# happens to sit in the caller's cwd — most likely nothing, which reads as a
# pass. Exit instead (SC2164).
cd "$REPO_ROOT" || { echo "FAIL: cannot cd to repo root $REPO_ROOT" >&2; exit 1; }

DEPENDABOT=.github/dependabot.yml
rc=0
fail() { echo "FAIL: $1" >&2; rc=1; }

# A missing tool is a hard failure, not a skip. Exiting 0 here would read as a
# PASS to preflight's shell_lib_tests (`bash "$t" || rc=1`), so the gate would
# go green having asserted nothing — the same "green report for work that never
# ran" changed-files_test.sh guards against, and the exact policy in
# security-scan.sh's header. All three tools are already hard requirements of
# the gates this test runs beside.
need() { command -v "$1" >/dev/null 2>&1 || { echo "FAIL: module-list_test — $1 not found; cannot verify the module lists agree" >&2; exit 1; }; }
need go
need jq

# Read one ecosystem's directories out of dependabot.yml, newline-separated and
# leading-slash-stripped so both sides of every comparison share a shape.
#
# Deliberately awk, not a YAML library. The first version used python3 with
# PyYAML: it passed locally and failed in CI, where python3 exists but PyYAML
# does not — `import yaml` raised, stderr was swallowed, and both ecosystems
# came back empty. Adding a pip install to CI to satisfy one assertion is a
# worse trade than parsing a file whose shape this repo controls.
#
# Brittleness is bounded by the emptiness guards below: a shape this parser
# cannot read yields nothing, and nothing is a loud failure rather than a
# vacuous match. That is how the CI break above surfaced at all.
dependabot_dirs() {
  awk -v want="$1" '
    # A new list item resets block state; capture the ecosystem it declares.
    /^[[:space:]]*-[[:space:]]*package-ecosystem:[[:space:]]*/ {
      eco = $NF; in_dirs = 0; next
    }
    # Plural form: `directories:` opens a list of paths.
    /^[[:space:]]*directories:[[:space:]]*$/ { in_dirs = (eco == want); next }
    # Singular form: `directory: /` carries its value inline.
    /^[[:space:]]*directory:[[:space:]]*/ {
      if (eco == want) { v = $NF; gsub(/^"|"$/, "", v); print v }
      in_dirs = 0; next
    }
    # Inside a directories: block, list items are the paths. Anything else
    # (schedule:, groups:, a comment) closes it — which is what keeps the
    # `- "*"` items under groups.patterns from being read as directories.
    {
      if (in_dirs && $0 ~ /^[[:space:]]*-[[:space:]]/) {
        v = $NF; gsub(/^"|"$/, "", v); print v
      } else if ($0 !~ /^[[:space:]]*-[[:space:]]/) {
        in_dirs = 0
      }
    }
  ' "$DEPENDABOT" |
    # Repo-relative, to match go.work's and WEB_TREES' shape. The repo root is
    # "/" in dependabot's spelling; stripping the slash would leave the empty
    # string, which the blank-line filter would then drop — silently losing an
    # ecosystem that watches the root (github-actions does). Map it to "." so
    # it survives as a real entry.
    sed 's|^/||; s|^$|.|' | sed '/^$/d' | sort
}

# --- gomod: dependabot must cover exactly go.work's modules -----------------
go_work_modules=$(go work edit -json | jq -r '.Use[].DiskPath | sub("^\\./"; "")' | sort)
gomod_dirs=$(dependabot_dirs gomod)

# Guard emptiness on both sides before comparing: two empty strings are equal,
# so an unreadable go.work plus a dependabot.yml with no gomod entry would
# otherwise report a positive match having compared nothing.
if [[ -z "$go_work_modules" ]]; then
  fail "could not read any module from go.work — the assertion below would compare nothing against nothing and call it a match"
elif [[ -z "$gomod_dirs" ]]; then
  fail "dependabot.yml declares no gomod directories — every Go module is unwatched"
elif [[ "$go_work_modules" != "$gomod_dirs" ]]; then
  fail "dependabot.yml's gomod directories do not match go.work's modules"
  diff <(echo "$go_work_modules") <(echo "$gomod_dirs") \
    | sed 's/^/  /; s/^  </  only in go.work: /; s/^  >/  only in dependabot.yml: /' >&2
fi

# --- npm: dependabot must cover exactly security-scan.sh's WEB_TREES --------
# Sourcing security-scan.sh would run the whole scan, so read the literal.
web_trees=$(sed -n 's/^WEB_TREES=(\(.*\))$/\1/p' tools/security-scan.sh | tr ' ' '\n' | sed '/^$/d' | sort)
npm_dirs=$(dependabot_dirs npm)

if [[ -z "$web_trees" ]]; then
  fail "could not read WEB_TREES from tools/security-scan.sh — the assertion below would compare against nothing"
elif [[ -z "$npm_dirs" ]]; then
  fail "dependabot.yml declares no npm directories — both web trees are unwatched"
elif [[ "$web_trees" != "$npm_dirs" ]]; then
  fail "dependabot.yml's npm directories do not match security-scan.sh's WEB_TREES"
  diff <(echo "$web_trees") <(echo "$npm_dirs") \
    | sed 's/^/  /; s/^  </  only in security-scan.sh: /; s/^  >/  only in dependabot.yml: /' >&2
fi

# --- security-scan.sh refuses to run on an unreadable module list -----------
# The derivation replaced a hardcoded array, so its "empty means broken, not
# 'no Go code'" guard is the single point where a silent zero-module scan gets
# caught. Pin it here rather than trusting a one-off manual check, the same
# reason #1231's guards landed with tests in changed-files_test.sh.
gostub=$(mktemp -d)
trap 'rm -rf "$gostub"' EXIT
printf '#!/bin/sh\nexit 1\n' > "$gostub/go"
chmod +x "$gostub/go"
guard_out=$(PATH="$gostub:$PATH" bash tools/security-scan.sh --local 2>&1)
guard_rc=$?
if [[ "$guard_rc" -ne 2 ]]; then
  fail "security-scan.sh should exit 2 when the module list cannot be read, got $guard_rc"
elif [[ "$guard_out" != *"refusing to report a pass"* ]]; then
  fail "security-scan.sh exited 2 but without naming the cause; got: $guard_out"
fi

[[ "$rc" -eq 0 ]] && echo "OK: module-list_test — dependabot.yml matches go.work and WEB_TREES"
exit "$rc"
