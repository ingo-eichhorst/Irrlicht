#!/usr/bin/env bash
# provider-census_test.sh — mutation evidence for tools/provider-census.sh
# (issue #2006). Per this repo's testing philosophy (AGENTS.md's Testing
# section): a check a change ADDS has no "before the fix" to run red
# against, so it owes a deliberate mutation instead, and a fixture must
# assert it actually hit its target rather than trusting a changed number.
#
# Five cases, each pinned to the exit code tools/provider-census.sh's own
# header documents:
#   the "good" fixture (offline)         — two small provider-list sources,
#                                           5 entries total: exit 0.
#   the mutation: remove one entry       — copies "good" to a scratch dir,
#                                           asserts the target entry is
#                                           present, deletes it with jq,
#                                           asserts it is gone, reruns, and
#                                           asserts the printed total is
#                                           EXACTLY one less — not merely
#                                           "different" — from the baseline.
#                                           This is the "never a hand-typed
#                                           count" mutation from issue #2006
#                                           §7.
#   the "unreachable" fixture (live)     — one provider-list source whose
#                                           check_url is 127.0.0.1:1 (nothing
#                                           listens there: curl fails with
#                                           connection-refused, offline and
#                                           online alike, no DNS/GitHub
#                                           dependency). Asserts exit 4, the
#                                           source id and revision are named,
#                                           and — the load-bearing assertion
#                                           — the output contains NO "TOTAL"
#                                           line, so an unreachable source
#                                           can never render as "0 providers
#                                           found" alongside a number that
#                                           looks complete.
#   the "empty-entries" fixture (offline)— one provider-list source whose
#                                           entries array is []. Asserts
#                                           exit 3, the source id is named,
#                                           and again no "TOTAL" line — the
#                                           second, distinct way an empty
#                                           list must not render as success.
#   missing inspected_paths (offline)    — found in code review of #2006:
#                                           --markdown dereferences
#                                           .inspected_paths unconditionally,
#                                           but it was not a required field,
#                                           so a source missing it crashed
#                                           that jq call silently (exit 0).
#                                           Reproduced by hand against the
#                                           actual pre-fix commit
#                                           (6b7a89f51) before this test was
#                                           committed — see the red-before-
#                                           green record at case 5 below for
#                                           why that reproduction is
#                                           documented rather than re-run
#                                           live (a CI checkout's fetch
#                                           depth, not the code, decides
#                                           whether `git show <old-sha>`
#                                           succeeds). The permanent,
#                                           always-run assertion is the
#                                           fixture-based green case: the
#                                           CURRENT script must refuse the
#                                           same fixture loudly (exit 3,
#                                           naming the source and field).
#
# Plus two vacuity guards: the real six-source docs/providers/sources/ must
# itself validate (exit 0, offline), and the count docs/providers/catalog.md
# states beside the command must equal what the command computes RIGHT NOW
# from that same directory — otherwise "never retype a count" would be
# violated the first time someone edits a source file and forgets the doc.
set -uo pipefail

REPO_ROOT=$(git rev-parse --show-toplevel)
cd "$REPO_ROOT" || { echo "FAIL: cannot cd to repo root $REPO_ROOT" >&2; exit 1; }

CENSUS=tools/provider-census.sh
FIXTURES=tools/lib/testdata/provider-census
rc=0
fail() { echo "FAIL: $1" >&2; rc=1; }
pass() { echo "  PASS: $1"; }

# --- case 1: the "good" fixture validates cleanly, offline ------------------
out=$(bash "$CENSUS" --offline --dir "$FIXTURES/good" 2>&1)
got=$?
if [[ "$got" -ne 0 ]]; then
  fail "good fixture (offline): expected exit 0, got $got — output: $out"
elif [[ "$out" != *"TOTAL candidate billing-product mentions across provider-list sources: 5"* ]]; then
  fail "good fixture (offline): exit 0 but total line missing/wrong — output: $out"
else
  pass "good fixture (offline) validates, total=5"
fi

# --- case 2: the mutation — remove one entry, total must drop by exactly 1 -
WORK=$(mktemp -d -t irrlicht-provider-census-test) || { fail "mktemp failed"; WORK=""; }
if [[ -n "$WORK" ]]; then
  cp "$FIXTURES/good"/*.json "$WORK/"
  before_total=$(bash "$CENSUS" --offline --dir "$WORK" 2>/dev/null \
    | sed -n 's/.*sources: \([0-9]*\)$/\1/p')
  if [[ "$before_total" != "5" ]]; then
    fail "mutation setup: baseline total over the copied fixture should be 5, got '$before_total'"
  else
    present=$(jq '[.entries[] | select(.id=="alpha")] | length' "$WORK/good-a.json")
    if [[ "$present" != "1" ]]; then
      fail "mutation setup: fixture copy does not contain the entry 'alpha' this mutation targets (found $present) — refusing to claim a mutation that never hit its target"
    else
      jq 'del(.entries[] | select(.id=="alpha"))' "$WORK/good-a.json" > "$WORK/good-a.json.tmp" \
        && mv "$WORK/good-a.json.tmp" "$WORK/good-a.json"
      still_present=$(jq '[.entries[] | select(.id=="alpha")] | length' "$WORK/good-a.json")
      if [[ "$still_present" != "0" ]]; then
        fail "mutation did not take: 'alpha' still present in $WORK/good-a.json after jq del — the mutation never hit its target"
      else
        after=$(bash "$CENSUS" --offline --dir "$WORK" 2>&1)
        after_rc=$?
        after_total=$(sed -n 's/.*sources: \([0-9]*\)$/\1/p' <<<"$after")
        if [[ "$after_rc" -ne 0 ]]; then
          fail "mutated fixture: expected exit 0 (still schema-valid, just one fewer entry), got $after_rc — output: $after"
        elif [[ "$after_total" != "4" ]]; then
          fail "mutated fixture: expected total EXACTLY one less than baseline 5 (i.e. 4), got '$after_total' — a count that merely 'differs' is not proof this came from the imported list rather than a hand-typed number"
        else
          pass "removing one entry from the imported list changes the command's count from 5 to 4 (not a hand-typed number)"
        fi
      fi
    fi
  fi
  rm -rf "$WORK"
fi

# --- case 3: the "unreachable" fixture (live network path exercised) -------
target_url=$(jq -r '.check_url' "$FIXTURES/unreachable/bad-source.json")
if [[ "$target_url" != "http://127.0.0.1:1/" ]]; then
  fail "unreachable fixture setup: check_url is '$target_url', not the intended http://127.0.0.1:1/ — refusing to claim this mutation targets what it says"
else
  out=$(bash "$CENSUS" --dir "$FIXTURES/unreachable" 2>&1)
  got=$?
  if [[ "$got" -ne 4 ]]; then
    fail "unreachable fixture: expected exit 4, got $got — output: $out"
  elif [[ "$out" != *"bad-source"* || "$out" != *"0000000000000000000000000000000000000c"* ]]; then
    fail "unreachable fixture: exit 4 but output does not name the source id and revision — output: $out"
  elif [[ "$out" == *"TOTAL"* ]]; then
    fail "unreachable fixture: output contains a TOTAL line — an unreachable source must never render alongside a total that looks complete — output: $out"
  else
    pass "unreachable pinned revision (127.0.0.1:1, connection-refused) fails loudly (exit 4), names the source, and prints no TOTAL line"
  fi
fi

# --- case 4: the "empty-entries" fixture (content failure, distinct path) --
out=$(bash "$CENSUS" --offline --dir "$FIXTURES/empty-entries" 2>&1)
got=$?
if [[ "$got" -ne 3 ]]; then
  fail "empty-entries fixture: expected exit 3, got $got — output: $out"
elif [[ "$out" != *"empty-source"* ]]; then
  fail "empty-entries fixture: exit 3 but output does not name the source id — output: $out"
elif [[ "$out" == *"TOTAL"* ]]; then
  fail "empty-entries fixture: output contains a TOTAL line — an empty imported list must never render as '0 providers found' alongside a total — output: $out"
else
  pass "a provider-list source with an empty entries array fails loudly (exit 3), distinct from the unreachable case (exit 4), and prints no TOTAL line"
fi

# --- case 5: missing inspected_paths (found in code review of #2006) -------
# `--markdown` unconditionally dereferences `.inspected_paths`, but the
# pre-fix REQUIRED_FIELDS list at commit 6b7a89f51 did not require it: a
# provider-list source missing that field passed schema validation and then
# crashed the per-file jq call inside the markdown loop — silently, because
# nothing checked that jq call's exit status, so the run still printed a
# (truncated) table and exited 0.
#
# Red-before-green record (not re-run live here, deliberately — see
# tools/lib/agents-md-lint_test.sh's own "red-before-green record" section
# for the same reasoning): a CI checkout is commonly shallow (GitHub
# Actions' default `actions/checkout` fetch depth), so `git show
# 6b7a89f51:tools/provider-census.sh` can fail there with "could not read"
# even though the object exists in a full clone — measured directly: this
# case failed exactly that way on this PR's own go-test run before this
# comment replaced the live `git show`. Pinning an automated, permanently-
# running check to one historical commit's reachability would make the
# gate's outcome depend on checkout depth rather than on the code, which is
# the same trap that comment already names. Reproduce by hand instead:
#   git show 6b7a89f51:tools/provider-census.sh > /tmp/prefix.sh
#   bash /tmp/prefix.sh --offline --markdown \
#     --dir tools/lib/testdata/provider-census/missing-inspected-paths
# — exits 0 with a swallowed `jq: error (... Cannot iterate over null ...)`
# on the pre-fix script; confirmed exactly this output before this test was
# committed. The fixture-based check below is the permanent, generic proof
# that fires regardless of git history: the CURRENT script must refuse the
# same fixture loudly.
out=$(bash "$CENSUS" --offline --markdown --dir "$FIXTURES/missing-inspected-paths" 2>&1)
got=$?
if [[ "$got" -ne 3 ]]; then
  fail "missing-inspected_paths fixture: expected exit 3 from the fixed script, got $got — output: $out"
elif [[ "$out" != *"no-paths-source"* || "$out" != *"inspected_paths"* ]]; then
  fail "missing-inspected_paths fixture: exit 3 but output does not name the source id and the missing field — output: $out"
elif [[ "$out" == *"jq: error"* ]]; then
  fail "missing-inspected_paths fixture: still leaking a raw jq crash — output: $out"
else
  pass "green: the fixed script refuses a provider-list source missing inspected_paths loudly (exit 3, naming the field) instead of crashing silently inside --markdown"
fi

# --- vacuity guard 1: the real six sources validate, offline ---------------
out=$(bash "$CENSUS" --offline 2>&1)
got=$?
if [[ "$got" -ne 0 ]]; then
  fail "the real docs/providers/sources/ tree does not validate offline (exit $got) — output: $out"
else
  pass "the real docs/providers/sources/ tree validates offline"
fi
real_total=$(sed -n 's/.*sources: \([0-9]*\)$/\1/p' <<<"$out")

# --- vacuity guard 2: docs/providers/catalog.md states the SAME number ----
# Without this, the doc's figure could drift from the command the moment
# someone edits a source file and forgets to rerun the command before
# editing the doc by hand — exactly the failure issue #2006 exists to close.
if [[ -z "$real_total" ]]; then
  fail "could not read a TOTAL from the real (offline) run to compare against the doc"
elif [[ ! -f docs/providers/catalog.md ]]; then
  fail "docs/providers/catalog.md does not exist"
else
  doc_total=$(grep -oE '\*\*[0-9]+\*\* candidate billing-product mentions' docs/providers/catalog.md \
    | head -1 | grep -oE '[0-9]+')
  if [[ -z "$doc_total" ]]; then
    fail "docs/providers/catalog.md does not state a '**N** candidate billing-product mentions' figure to check against the command"
  elif [[ "$doc_total" != "$real_total" ]]; then
    fail "docs/providers/catalog.md states $doc_total candidates but tools/provider-census.sh --offline currently reports $real_total — the doc was hand-edited out of sync with the command it cites"
  else
    pass "docs/providers/catalog.md's stated count ($doc_total) matches tools/provider-census.sh --offline's live count"
  fi
fi

[[ "$rc" -eq 0 ]] && echo "OK: provider-census_test — loud-failure rule (unreachable vs empty-list, both distinct from success), mutation-changes-the-count rule, and the doc/command count lock all hold"
exit "$rc"
