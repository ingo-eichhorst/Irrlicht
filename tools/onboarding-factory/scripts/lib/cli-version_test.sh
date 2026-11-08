#!/usr/bin/env bash
# cli-version_test.sh — unit tests for cli-version.sh's resolve_cli_version.
# Plain bash (no framework). Run directly or via scripts/smoke-test.sh.
#
# The load-bearing case is the first: the opencode-v2 column (#2109) reads its
# version from the 2.x store and runs NO binary. Stub `opencode`/`opencode2`
# executables sit first on PATH and leave a marker if anything runs them, so
# "nothing ran" is observed rather than assumed. sqlite3 is the real one — the
# store read is the subject.

set -uo pipefail   # NOT -e: assertions capture non-zero return codes

DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$DIR/../../../.." && pwd)"
# shellcheck source=cli-version.sh
source "$DIR/cli-version.sh"

fails=0
pass() { local label="$1"; echo "  PASS: $label"; return 0; }
fail() { local label="$1" detail="$2"; echo "  FAIL: $label — $detail"; fails=$((fails + 1)); return 0; }
assert_eq() {
  local label="$1" expected="$2" got="$3"
  if [[ "$got" == "$expected" ]]; then pass "$label"; else fail "$label" "expected [$expected] got [$got]"; fi
  return 0
}

command -v sqlite3 >/dev/null 2>&1 || { echo "cli-version_test: sqlite3 is required and not on PATH — this test cannot run" >&2; exit 1; }

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# Stubs that betray any execution of an opencode binary.
STUBS="$TMP/stubs"; mkdir -p "$STUBS"
RAN="$TMP/ran"
for b in opencode opencode2; do
  printf '#!/bin/sh\necho "%s $*" >> "%s"\necho "9.9.9"\n' "$b" "$RAN" > "$STUBS/$b"
  chmod +x "$STUBS/$b"
done
printf '#!/bin/sh\necho "fakecli v1.2.3."\n' > "$STUBS/fakecli"; chmod +x "$STUBS/fakecli"
PATH="$STUBS:$PATH"

# A 2.x store built from the 2.1.4 DDL the opencode adapter's own tests use.
DDL="$ROOT/core/adapters/inbound/agents/opencode/testdata/v2-schema.sql"
DATA="$TMP/data"; mkdir -p "$DATA/opencode"
DB="$DATA/opencode/opencode.db"
if ! sqlite3 "$DB" < "$DDL"; then
  echo "cli-version_test: could not build the scratch store from $DDL — this test cannot run" >&2
  exit 1
fi
sqlite3 "$DB" "INSERT INTO session_v2 (id, project_id, slug, directory, version, time_created, time_updated)
  VALUES ('ses_old', 'p', 's', '/w', '2.0.3', 100, 100), ('ses_new', 'p', 's', '/w', '2.1.4', 200, 200);"

echo "== opencode-v2 (@store): the newest session_v2.version, and no binary runs =="
got="$(OPENCODE_DB='' XDG_DATA_HOME="$DATA" resolve_cli_version opencode-v2 @store 0 2>&1)"
assert_eq "reads the newest row's version" "2.1.4" "$got"
if [[ -e "$RAN" ]]; then
  fail "no opencode binary ran" "the stub recorded: $(tr '\n' ';' < "$RAN")"
else
  pass "no opencode binary ran"
fi

echo "== opencode-v2 refuses rather than read a different store than the 2.x service writes =="
out="$(OPENCODE_DB=/x/other.db XDG_DATA_HOME="$DATA" resolve_cli_version opencode-v2 @store 0 2>&1)"; rc=$?
assert_eq "OPENCODE_DB set: exit 1" "1" "$rc"
[[ "$out" == *"OPENCODE_DB is set"* ]] && pass "OPENCODE_DB set: says why" || fail "OPENCODE_DB set: says why" "$out"
out="$(unset XDG_DATA_HOME; OPENCODE_DB='' resolve_cli_version opencode-v2 @store 0 2>&1)"; rc=$?
assert_eq "XDG_DATA_HOME unset: exit 1" "1" "$rc"
out="$(OPENCODE_DB='' XDG_DATA_HOME=relative/data resolve_cli_version opencode-v2 @store 0 2>&1)"; rc=$?
assert_eq "XDG_DATA_HOME relative: exit 1" "1" "$rc"
EMPTY="$TMP/empty"; mkdir -p "$EMPTY/opencode"; sqlite3 "$EMPTY/opencode/opencode.db" < "$DDL"
out="$(OPENCODE_DB='' XDG_DATA_HOME="$EMPTY" resolve_cli_version opencode-v2 @store 0 2>&1)"; rc=$?
assert_eq "a store with no 2.x row: exit 1" "1" "$rc"
[[ "$out" == *"no session_v2 row"* ]] && pass "no 2.x row: says why" || fail "no 2.x row: says why" "$out"
if [[ -e "$RAN" ]]; then fail "still no opencode binary ran" "$(tr '\n' ';' < "$RAN")"; else pass "still no opencode binary ran"; fi

echo "== an @store arm for an adapter with no store is refused, not guessed =="
out="$(resolve_cli_version codex @store 0 2>&1)"; rc=$?
assert_eq "exit 1" "1" "$rc"

echo "== a binary arm: field, trailing period and leading v =="
assert_eq "fakecli field 2" "1.2.3" "$(resolve_cli_version fake fakecli 2 2>&1)"
out="$(resolve_cli_version fake no-such-cli-2109 1 2>&1)"; rc=$?
assert_eq "a missing binary: exit 1" "1" "$rc"

echo ""
if [[ "$fails" -eq 0 ]]; then
  echo "cli-version_test: ALL PASS"
else
  echo "cli-version_test: $fails FAILED" >&2
  exit 1
fi
