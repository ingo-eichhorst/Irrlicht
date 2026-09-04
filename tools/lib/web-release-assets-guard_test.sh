#!/usr/bin/env bash
# web-release-assets-guard_test.sh — the lock over tools/web-release-assets-guard.sh
# and tools/lib/stage-web.sh (#1900).
#
# It grades three things:
#
#   1. THE REAL REPO. The guard runs clean against this checkout, and the
#      closure it computes really does contain collapsedSet.js — the module
#      NOTHING imports directly, which is what separates a transitive walk
#      from a scan of irrlicht.js's direct imports that merely looks right.
#      A direct-only scan finds 9 of the 10 modules and calls itself complete.
#
#   2. THE STAGING RULE, against synthetic trees: a newly extracted module
#      ships by default, and dev-only tooling never does.
#
#   3. FAIL-LOUDLY. Every way the guard can be unable to look must REFUSE (2),
#      never report success on an empty answer: no tree, no index.html, an
#      index.html with no entry point, a module graph with no import edge, a
#      reference to a file that does not exist, and a reference into a
#      subdirectory that the non-recursive staging rule would silently drop.
#      That last pair is not hypothetical bookkeeping — the whole defect this
#      guard exists for was a staging step that silently dropped nine files.
#
# The mutation half — breaking the protected thing and confirming this file
# goes red — lives in tools/lib/web-release-assets-guard-mutations_test.sh.

set -uo pipefail

REPO_ROOT=$(git rev-parse --show-toplevel 2>/dev/null) || {
  echo "FAIL: web-release-assets-guard_test — not inside a git repository" >&2
  exit 1
}
cd "$REPO_ROOT" || {
  echo "FAIL: web-release-assets-guard_test — cannot cd to repo root $REPO_ROOT" >&2
  exit 1
}

GUARD=tools/web-release-assets-guard.sh
for f in "$GUARD" tools/lib/stage-web.sh platforms/web/index.html; do
  [[ -f "$f" ]] || { echo "FAIL: web-release-assets-guard_test — $f is missing; the subject is gone" >&2; exit 1; }
done

# shellcheck source=../web-release-assets-guard.sh
. "$REPO_ROOT/$GUARD"
# shellcheck source=stage-web.sh
. "$REPO_ROOT/tools/lib/stage-web.sh"

TMP=$(mktemp -d -t web-release-assets-guard_test) || exit 1
trap 'rm -rf "$TMP"' EXIT

rc=0
fail() { echo "FAIL: $1" >&2; rc=1; }
pass() { echo "  PASS: $1"; }

# expect_refuse <label> <src-dir> <fragment> — the closure must REFUSE (2) and
# say why. A guard that returned 0 with an empty closure would satisfy every
# "nothing is missing" assertion in this file, so the status AND the reason
# are both asserted.
expect_refuse() {
  local label="$1" dir="$2" want="$3" out st
  out=$(web_assets_closure "$dir" 2>&1)
  st=$?
  if [[ "$st" -ne 2 ]]; then
    fail "$label — expected REFUSE (2), got status $st with: $(echo "$out" | tr '\n' ' ')"
    return
  fi
  case "$out" in
    *"$want"*) pass "$label" ;;
    *) fail "$label — refused, but not for the stated reason (wanted: $want); got: $(echo "$out" | tr '\n' ' ')" ;;
  esac
}

# ---------------------------------------------------------------------------
# 1. The real repository.

echo "== the guard runs clean against this checkout =="
guard_out=$(bash "$GUARD" 2>&1)
guard_st=$?
if [[ "$guard_st" -ne 0 ]]; then
  fail "the guard must be green on a correct tree — exit $guard_st"
  echo "$guard_out" | sed 's/^/      | /' >&2
else
  pass "tools/web-release-assets-guard.sh exits 0 on this checkout"
fi

echo "== the closure over the REAL platforms/web is transitive and non-empty =="
real_closure=$(web_assets_closure platforms/web 2>&1)
real_st=$?
if [[ "$real_st" -ne 0 ]]; then
  fail "the closure over the real web tree must succeed — exit $real_st: $(echo "$real_closure" | tr '\n' ' ')"
else
  n=$(printf '%s\n' "$real_closure" | grep -c . )
  # A vacuity guard on this file itself: a closure that shrank to a handful of
  # entries would still satisfy the membership assertions below.
  if [[ "$n" -lt 10 ]]; then
    fail "the real closure has only $n entries — this file is no longer examining the real dashboard"
  else
    pass "the real closure has $n entries"
  fi
  for want in index.html irrlicht.css irrlicht.js collapsedSet.js; do
    case $'\n'"$real_closure"$'\n' in
      *$'\n'"$want"$'\n'*) pass "closure contains $want" ;;
      *) fail "closure over the real tree is missing $want" ;;
    esac
  done
  # The load-bearing one, spelled out: collapsedSet.js is imported by
  # collapsedGroups.js and collapsedSummaries.js and by nothing else, so it is
  # present ONLY if the walk is transitive.
  if ! grep -aq "collapsedSet.js" platforms/web/collapsedGroups.js; then
    fail "collapsedGroups.js no longer imports collapsedSet.js — this file's transitivity claim rests on an edge that is gone; re-pick the depth-3 module"
  else
    pass "collapsedSet.js is still reachable only transitively (via collapsedGroups.js)"
  fi
  if grep -aq "from '\./collapsedSet\.js'" platforms/web/irrlicht.js; then
    fail "irrlicht.js now imports collapsedSet.js directly — a direct-import scan would pass, so this file's transitivity assertion no longer distinguishes anything"
  else
    pass "irrlicht.js does not import collapsedSet.js directly"
  fi
fi

echo "== the real tree stages exactly the closure, and nothing dev-only =="
staged=$(stage_web_list platforms/web 2>&1)
if [[ $? -ne 0 ]]; then
  fail "stage_web_list refused on the real tree: $(echo "$staged" | tr '\n' ' ')"
else
  # platforms/web holds test files today; if it ever stops, this assertion
  # stops meaning anything and says so rather than passing quietly.
  if ! ls platforms/web/*.test.js >/dev/null 2>&1; then
    fail "platforms/web has no *.test.js at all — the dev-only exclusion is no longer being exercised by the real tree"
  else
    case $'\n'"$staged"$'\n' in
      *.test.js*) fail "the staging rule would ship a *.test.js from the real tree" ;;
      *) pass "no *.test.js in the real staged set" ;;
    esac
  fi
  case $'\n'"$staged"$'\n' in
    *$'\n'vitest.*) fail "the staging rule would ship a vitest.* file" ;;
    *) pass "no vitest.* in the real staged set" ;;
  esac
fi

# ---------------------------------------------------------------------------
# 2. The staging rule, on synthetic trees.

echo "== a newly extracted module ships without anyone listing it =="
NEW="$TMP/newmodule"
mkdir -p "$NEW"
printf '<!doctype html>\n<link rel="stylesheet" href="app.css">\n<script type="module" src="app.js"></script>\n' >"$NEW/index.html"
printf "import { x } from './brandNew.js';\n" >"$NEW/app.js"
printf 'export const x = 1;\n' >"$NEW/brandNew.js"
: >"$NEW/app.css"
new_list=$(stage_web_list "$NEW")
case $'\n'"$new_list"$'\n' in
  *$'\n'brandNew.js$'\n'*) pass "a module nobody listed is staged by the rule" ;;
  *) fail "the staging rule dropped brandNew.js — it is a list again, not a rule" ;;
esac
new_closure=$(web_assets_closure "$NEW")
case $'\n'"$new_closure"$'\n' in
  *$'\n'brandNew.js$'\n'*) pass "the walk reaches a module through one hop" ;;
  *) fail "the walk missed brandNew.js: $(echo "$new_closure" | tr '\n' ' ')" ;;
esac

echo "== dev-only tooling is excluded, and directories cannot be swept in =="
mkdir -p "$NEW/node_modules" "$NEW/snapshots"
: >"$NEW/node_modules/pkg.js"
: >"$NEW/snapshots/serialize.js"
: >"$NEW/app.test.js"
: >"$NEW/vitest.config.js"
: >"$NEW/vitest.setup.js"
: >"$NEW/package.json"
dev_list=$(stage_web_list "$NEW")
for bad in app.test.js vitest.config.js vitest.setup.js package.json pkg.js serialize.js; do
  case $'\n'"$dev_list"$'\n' in
    *$'\n'"$bad"$'\n'*) fail "the staging rule would ship dev-only $bad" ;;
    *) pass "excluded: $bad" ;;
  esac
done

echo "== a module carrying a literal NUL byte is still scanned =="
# Not hypothetical: platforms/web/irrlicht.js and sessionIdentity.js both hold
# literal NULs (an id separator, `daemonId + '\0' + sessionId`), and a
# binary-detecting grep answers "Binary file … matches" instead of the matches
# — exit 0, no usable output. That is the shape of vacuity this whole guard is
# built against, so it is pinned on a synthetic module whose NUL sits in the
# first read block, where every grep implementation detects it.
NUL="$TMP/nulmodule"
mkdir -p "$NUL"
printf '<script type="module" src="app.js"></script>\n' >"$NUL/index.html"
printf 'const SEP = "\000";\nimport { x } from '\''./dep.js'\'';\n' >"$NUL/app.js"
printf 'export const x = 1;\n' >"$NUL/dep.js"
if ! LC_ALL=C grep -qU $'\000' "$NUL/app.js" 2>/dev/null && ! LC_ALL=C grep -qaP '\x00' "$NUL/app.js" 2>/dev/null; then
  # Neither probe could confirm the byte survived the write, so the assertion
  # below would be testing an ordinary text file and passing for free.
  fail "the NUL fixture carries no NUL byte — this assertion is vacuous"
fi
nul_closure=$(web_assets_closure "$NUL" 2>&1)
nul_st=$?
if [[ "$nul_st" -ne 0 ]]; then
  fail "the walk refused on a module containing a NUL byte (exit $nul_st): $(echo "$nul_closure" | tr '\n' ' ')"
else
  case $'\n'"$nul_closure"$'\n' in
    *$'\n'dep.js$'\n'*) pass "an import inside a NUL-containing module is still found" ;;
    *) fail "the walk missed dep.js in a NUL-containing module — the scan is reading it as binary: $(echo "$nul_closure" | tr '\n' ' ')" ;;
  esac
fi

echo "== the walk terminates on a cyclic graph =="
CYC="$TMP/cyclic"
mkdir -p "$CYC"
printf '<script type="module" src="a.js"></script>\n' >"$CYC/index.html"
printf '<!doctype html>\n%s' "$(cat "$CYC/index.html")" >"$CYC/index.html.tmp" && mv "$CYC/index.html.tmp" "$CYC/index.html"
printf "import { b } from './b.js';\n" >"$CYC/a.js"
printf "import { a } from './a.js';\nimport { c } from './c.js';\n" >"$CYC/b.js"
printf "import { a } from './a.js';\n" >"$CYC/c.js"
cyc=$( ( web_assets_closure "$CYC" ) & pid=$!
  # A walk without a visited set does not terminate; bound it rather than
  # hanging the suite, and report the timeout as the finding it is.
  waited=0
  while kill -0 "$pid" 2>/dev/null && [[ "$waited" -lt 100 ]]; do
    sleep 0.1; waited=$((waited + 1))
  done
  if kill -0 "$pid" 2>/dev/null; then kill -9 "$pid" 2>/dev/null; echo "__TIMEOUT__"; fi
  wait "$pid" 2>/dev/null )
case "$cyc" in
  *__TIMEOUT__*) fail "the walk did not terminate on a cyclic graph within 10s — the visited set is gone" ;;
  *)
    for want in a.js b.js c.js; do
      case $'\n'"$cyc"$'\n' in
        *$'\n'"$want"$'\n'*) ;;
        *) fail "the cyclic walk missed $want: $(echo "$cyc" | tr '\n' ' ')" ;;
      esac
    done
    pass "the walk terminates on a cyclic graph and reaches every node"
    ;;
esac

# ---------------------------------------------------------------------------
# 3. Fail-loudly: inability to look never reads as success.

echo "== inability to look REFUSES rather than reporting an empty closure =="
expect_refuse "a source tree that does not exist" \
  "$TMP/absent" "source tree not found"

mkdir -p "$TMP/noindex"; : >"$TMP/noindex/app.js"
expect_refuse "a tree with no index.html" \
  "$TMP/noindex" "index.html is missing"

mkdir -p "$TMP/noentry"; printf '<!doctype html>\n<p>nothing here</p>\n' >"$TMP/noentry/index.html"
expect_refuse "an index.html with no <script src> or <link href>" \
  "$TMP/noentry" "no <script src> or <link href>"

mkdir -p "$TMP/nojs"; printf '<link rel="stylesheet" href="a.css">\n' >"$TMP/nojs/index.html"; : >"$TMP/nojs/a.css"
expect_refuse "an index.html that reaches no module at all" \
  "$TMP/nojs" "reached no JavaScript module"

# The vacuity case this guard was written against: a module scan that finds
# nothing looks byte-identical to a module graph that is fully staged.
mkdir -p "$TMP/noedge"
printf '<script type="module" src="app.js"></script>\n' >"$TMP/noedge/index.html"
printf 'export const x = 1;\n' >"$TMP/noedge/app.js"
expect_refuse "a module graph with not one import edge" \
  "$TMP/noedge" "not one import edge"

mkdir -p "$TMP/broken"
printf '<script type="module" src="app.js"></script>\n' >"$TMP/broken/index.html"
printf "import { x } from './gone.js';\n" >"$TMP/broken/app.js"
expect_refuse "an import of a file that does not exist" \
  "$TMP/broken" "does not exist in"

mkdir -p "$TMP/subdir/sub"
printf '<script type="module" src="app.js"></script>\n' >"$TMP/subdir/index.html"
printf "import { x } from './sub/deep.js';\n" >"$TMP/subdir/app.js"
printf 'export const x = 1;\n' >"$TMP/subdir/sub/deep.js"
expect_refuse "an import from a subdirectory the non-recursive rule cannot ship" \
  "$TMP/subdir" "non-recursive"

echo "== stage_web refuses loudly rather than staging an empty tree =="
out=$(stage_web "$TMP/absent" "$TMP/out" 2>&1); st=$?
[[ "$st" -eq 2 ]] || fail "stage_web on a missing source must return 2, got $st"
mkdir -p "$TMP/emptytree"
out=$(stage_web "$TMP/emptytree" "$TMP/out" 2>&1); st=$?
[[ "$st" -eq 3 ]] || fail "stage_web on a tree with no runtime asset must return 3, got $st"
case "$out" in *"found nothing to ship"*) ;; *) fail "stage_web's empty-tree refusal must say so; got: $out" ;; esac
out=$(stage_web "$TMP/noindex" "$TMP/out" 2>&1); st=$?
[[ "$st" -eq 4 ]] || fail "stage_web on a tree with no index.html must return 4, got $st"
out=$(stage_web platforms/web "" 2>&1); st=$?
[[ "$st" -eq 2 ]] || fail "stage_web with no destination must return 2, got $st"
[[ "$rc" -eq 0 ]] && pass "stage_web's four refusals are distinguishable by status"

# ---------------------------------------------------------------------------
# 4. Gate parity: the guard has to FIRE on the commit shape that breaks it.
#
# A gate that is silently SKIPped on the relevant diff is this issue one level
# up — and the relevant diff here is "someone extracted a new module", which
# touches platforms/web/ and nothing else. tools/preflight.sh --changed decides
# with `grep -qE <regex>` over the changed set, so the assertion below runs
# exactly that grep against exactly the regexes preflight.sh carries.

echo "== preflight --changed selects these gates for a platforms/web-only diff =="
# preflight_regex <name-fragment> — the single-quoted first argument of the
# run_gate_scoped call whose gate name contains <name-fragment>.
preflight_regex() {
  awk -v want="$1" '
    /run_gate_scoped/ { pending = $0; next }
    pending != "" {
      if (index($0, want)) {
        line = pending
        sub(/^[^\x27]*\x27/, "", line)
        sub(/\x27[^\x27]*$/, "", line)
        print line
        exit
      }
      pending = ""
    }
  ' tools/preflight.sh
}
for gate in "tools/lib shell-lib tests" "web release assets"; do
  re=$(preflight_regex "$gate")
  if [[ -z "$re" ]]; then
    # Could not look: the gate was renamed or the call reshaped. That is not
    # the same as "the trigger is wrong", and must not read as either a pass
    # or as the finding below.
    fail "could not extract a trigger regex for the '$gate' gate from tools/preflight.sh — this assertion cannot judge anything"
    continue
  fi
  if printf '%s\n' 'platforms/web/newlyExtractedModule.js' | grep -qE "$re"; then
    pass "'$gate' fires on a diff that only adds platforms/web/newlyExtractedModule.js"
  else
    fail "'$gate' would be SKIPped by preflight --changed for a diff touching only platforms/web/ — the trigger regex is missing ^platforms/web/, so the gate stops firing on exactly the commit shape that caused #1900. regex: $re"
  fi
done

[[ "$rc" -eq 0 ]] && echo "OK: web-release-assets-guard_test — the release web tree is staged by rule, complete under a transitive cycle-tolerant walk, free of dev-only files, every inability to look refuses out loud, and both gates fire on a platforms/web-only diff"
exit "$rc"
