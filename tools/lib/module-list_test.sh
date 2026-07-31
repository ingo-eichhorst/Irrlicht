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

need() { command -v "$1" >/dev/null 2>&1 || { echo "SKIP: module-list_test — $1 not found"; exit 0; }; }
need go
need jq
need python3

# Read one ecosystem's directories out of dependabot.yml, newline-separated and
# leading-slash-stripped so both sides of every comparison share a shape.
dependabot_dirs() {
  python3 -c '
import sys, yaml
eco = sys.argv[1]
cfg = yaml.safe_load(open(sys.argv[2]))
for u in cfg.get("updates", []):
    if u.get("package-ecosystem") == eco:
        for d in (u.get("directories") or [u.get("directory")]):
            if d:
                print(d.lstrip("/"))
' "$1" "$DEPENDABOT" 2>/dev/null | sort
}

# --- gomod: dependabot must cover exactly go.work's modules -----------------
go_work_modules=$(go work edit -json | jq -r '.Use[].DiskPath | sub("^\\./"; "")' | sort)
gomod_dirs=$(dependabot_dirs gomod)

if [[ "$go_work_modules" != "$gomod_dirs" ]]; then
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
elif [[ "$web_trees" != "$npm_dirs" ]]; then
  fail "dependabot.yml's npm directories do not match security-scan.sh's WEB_TREES"
  diff <(echo "$web_trees") <(echo "$npm_dirs") \
    | sed 's/^/  /; s/^  </  only in security-scan.sh: /; s/^  >/  only in dependabot.yml: /' >&2
fi

[[ "$rc" -eq 0 ]] && echo "OK: module-list_test — dependabot.yml matches go.work and WEB_TREES"
exit "$rc"
