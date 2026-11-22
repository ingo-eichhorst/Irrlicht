#!/usr/bin/env bash
# vibe-hook-variants-mutations_test.sh — the committed mutation fixtures for
# what #2171 ADDED, which has no "before the fix" to run red
# (docs/testing-philosophy.md: "anything a change ADDS ... owes a deliberate
# mutation instead"):
#
#   1. hooktoml.DeleteTopLevelKeyIfPresent (core/adapters/inbound/agents/
#      hooktoml/scalar.go), a byte-range delete: its line terminator, and
#      its loop over duplicate copies.
#   2. The axis guard of TestDeleteTopLevelKey_PropertyRandomMutations
#      (property_test.go): the property must fail when the CRLF axis it
#      exists for stops being generated.
#   3. installedVersionFromUvTool's uv tool directory resolution
#      (core/adapters/inbound/agents/vibe/uvtool.go): XDG_DATA_HOME.
#   4. The legacy Verify's reordered switch (core/adapters/inbound/agents/
#      vibe/hookinstaller.go), whose change from main's order no defect test
#      can show red (#2171 review finding 4).
#
# The defect tests #2171 added (hookvariant_test.go) were seen red against
# the code before the fix instead, and are not repeated here.
#
# Follows tools/lib/cost-unattributed-mutations_test.sh: same
# assert_go_test_goes_red shape, same tools/mutate.sh underneath, which owns
# the stale/ambiguous-anchor guards and the byte-for-byte restore that never
# touches git state.

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
MUTATE_SH="$REPO_ROOT/tools/mutate.sh"

# A missing tool is a hard failure, not a skip — exiting 0 here would read as
# a PASS to preflight's shell_lib_tests.
need() {
  local tool="$1"
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "FAIL: vibe-hook-variants-mutations — $tool not found" >&2
    exit 1
  fi
  return 0
}

quote_output() {
  sed 's/^/      | /'
  return 0
}
need go
need git

if [[ ! -x "$MUTATE_SH" ]]; then
  echo "FAIL: vibe-hook-variants-mutations — $MUTATE_SH is missing or not executable" >&2
  exit 1
fi

# mutate.sh refuses against a dirty tree. A dirty tree must not silently
# pass: a hard failure in CI or strict mode, a loud skip on a developer's
# dirty worktree (the same rule cost-unattributed-mutations_test.sh states).
if [[ -n "$(git -C "$REPO_ROOT" status --porcelain)" ]]; then
  echo "vibe-hook-variants-mutations: CANNOT RUN — the worktree is dirty, and mutate.sh needs a" >&2
  echo "  clean tree for its post-restore check to mean anything. Commit or clean up, then:" >&2
  echo "    bash tools/lib/vibe-hook-variants-mutations_test.sh" >&2
  if [[ -n "${CI:-}" || -n "${MUTATION_FIXTURES_STRICT:-}" ]]; then
    echo "  (failing rather than skipping: CI/strict mode)" >&2
    exit 1
  fi
  echo "  (skipped locally; set MUTATION_FIXTURES_STRICT=1 to make this a failure)" >&2
  exit 0
fi

fails=0

# assert_go_test_goes_red <label> <file> <anchor> <replacement> <pkg> <run-regex> <want-in-output>
#
# Applies one mutation and requires the named Go test to FAIL under it, and
# for the right reason: not a build failure, and with the expected message.
assert_go_test_goes_red() {
  local label="$1" file="$2" anchor="$3" replacement="$4" pkg="$5" run="$6" want="$7"
  local out rc

  out="$(cd "$REPO_ROOT" && "$MUTATE_SH" "$file" "$anchor" "$replacement" \
    bash -c "go test $pkg -run '$run' -count=1 2>&1; echo GO_TEST_RC=\$?" 2>&1)"
  rc=$?

  if [[ $rc -ne 0 ]]; then
    echo "FAIL: $label — mutate.sh refused (exit $rc). A STALE anchor means the surrounding text"
    echo "      moved and this fixture needs its anchor updated; it does NOT mean the guard is fine."
    echo "$out" | quote_output
    fails=$((fails + 1))
    return
  fi
  if grep -q 'GO_TEST_RC=0' <<<"$out"; then
    echo "FAIL: $label — the test stayed GREEN under the mutation, so it does not reach"
    echo "      what it claims to protect."
    echo "$out" | quote_output
    fails=$((fails + 1))
    return
  fi
  if grep -qE '^# |build failed|cannot use|undefined:' <<<"$out"; then
    echo "FAIL: $label — the mutation broke the BUILD rather than the guarded behaviour."
    echo "$out" | quote_output
    fails=$((fails + 1))
    return
  fi
  if ! grep -qF -- "$want" <<<"$out"; then
    echo "FAIL: $label — the test failed, but not with the expected message."
    echo "      wanted to find: $want"
    echo "$out" | quote_output
    fails=$((fails + 1))
    return
  fi
  echo "ok  $label"
}

HOOKTOML="core/adapters/inbound/agents/hooktoml/scalar.go"
HOOKTOML_PROPERTY="core/adapters/inbound/agents/hooktoml/property_test.go"
HOOKTOML_PKG="./core/adapters/inbound/agents/hooktoml/"

# ── the delete keeps the line's own newline ─────────────────────────────────
# A delete that cuts only up to the '\n' leaves an empty line where the key
# was. Every unrelated snippet survives that, so only the property's
# byte-exact grade can see it.
assert_go_test_goes_red \
  "a delete that leaves the key's newline behind reddens the byte-exact property" \
  "$HOOKTOML" \
  $'\t\tif end < len(out) {\n\t\t\tend++' \
  $'\t\tif false && end < len(out) {\n\t\t\tend++' \
  "$HOOKTOML_PKG" \
  "TestDeleteTopLevelKey_PropertyRandomMutations" \
  'result is not the input minus the one line'

# ── the delete stops after the first copy ───────────────────────────────────
# Verify reads any remaining copy as damage, so a delete that removes one
# copy per call would be re-run by every #1372 pass without converging.
assert_go_test_goes_red \
  "a delete that removes only the first duplicate reddens the duplicate case" \
  "$HOOKTOML" \
  $'\t\tout = spliceReplace(out, start, end, nil)\n\t}' \
  $'\t\tout = spliceReplace(out, start, end, nil)\n\t\tbreak\n\t}' \
  "$HOOKTOML_PKG" \
  "TestDeleteTopLevelKeyIfPresent/every_duplicate_copy" \
  'got "a = 1\nenable_experimental_hooks = false\n", want "a = 1\n"'

# ── the property stops generating CRLF documents ────────────────────────────
assert_go_test_goes_red \
  "a property that never generates CRLF trips its own axis guard" \
  "$HOOKTOML_PROPERTY" \
  $'\tif rng.Intn(4) == 0 {\n\t\tc.doc = bytes.ReplaceAll' \
  $'\tif rng.Intn(4) == 99 {\n\t\tc.doc = bytes.ReplaceAll' \
  "$HOOKTOML_PKG" \
  "TestDeleteTopLevelKey_PropertyRandomMutations" \
  'an axis this property exists for was barely exercised'

# ── the legacy Verify checks the gate before the block's shape ──────────────
# After a downgrade the current artifact (a post_agent block, no flag) must read
# Stale under a legacy-selected version, as agent.HookVariant.Verify's doc
# requires of another variant's file. Main's order, the gate first, read it as
# Missing, and no test noticed (#2171 review finding 4).
assert_go_test_goes_red \
  "a legacy Verify that checks the gate first reads the current artifact as Missing" \
  "core/adapters/inbound/agents/vibe/hookinstaller.go" \
  $'\tcase !present:\n\t\treturn agent.HookEntryStatus{Missing: []string{hookEventPostAgentTurn}}, nil' \
  $'\tcase !present || !flagOn:\n\t\treturn agent.HookEntryStatus{Missing: []string{hookEventPostAgentTurn}}, nil' \
  "./core/adapters/inbound/agents/vibe/" \
  "TestLegacyVibe_ACurrentArtifactReadsStale" \
  'want Stale only'

# ── the version source ignores XDG_DATA_HOME ────────────────────────────────
assert_go_test_goes_red \
  "an Observed that ignores XDG_DATA_HOME reads the default tree instead" \
  "core/adapters/inbound/agents/vibe/uvtool.go" \
  $'if v := os.Getenv("XDG_DATA_HOME"); v != "" && filepath.IsAbs(v) {' \
  $'if v := os.Getenv("XDG_DATA_HOME"); false && v != "" && filepath.IsAbs(v) {' \
  "./core/adapters/inbound/agents/vibe/" \
  "TestObservedReadsTheUvToolVersion/XDG_DATA_HOME" \
  'want the XDG_DATA_HOME tree'"'"'s 2.21.0'

if [[ $fails -gt 0 ]]; then
  echo "vibe-hook-variants-mutations: $fails FAILED"
  exit 1
fi
echo "vibe-hook-variants-mutations: ALL PASS"
