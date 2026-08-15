#!/usr/bin/env bash
# security-scan.sh — vulnerability scan + SAST gate, shared by
# tools/preflight.sh's `security` group and .claude/skills/ir:release/SKILL.md's
# pre-build gate (Step 5.5), so both surfaces run identical checks.
#
# Full mode (default, used by /ir:release):
#   - open GitHub Dependabot alerts (Critical/High)
#   - open GitHub CodeQL code-scanning alerts (Critical/High)
#   - govulncheck ./...   (every module in go.work)
#   - gosec ./...         (every module in go.work)
#   - npm audit --audit-level=high (both web trees)
#
# Local mode (--local, used by preflight.sh's pre-push gate): skips the two
# GitHub API calls — no repo/network dependency, and Dependabot/CodeQL alerts
# aren't meaningfully different push-to-push — and runs govulncheck + gosec +
# npm audit only.
#
# Changed-scoped mode (--changed, added by preflight.sh --changed on top of
# --local): each scanner runs only where this branch touched that scanner's
# own inputs — govulncheck/gosec per Go module with a changed .go/go.mod/go.sum,
# npm audit per web tree with a changed package.json/package-lock.json. A diff
# that changes neither cannot introduce a finding there, so scanning it only
# imports a pre-existing, unrelated advisory into an unrelated push — which
# rejected every push on a Go-only branch and forced --no-verify, disabling
# every other gate too (issue #1213). Skips are printed, never silent, per the
# "can't-run is not a skip" policy below. Without --changed nothing narrows:
# the default run (and /ir:release's Step 5.5) still scans everything.
#
# Severity semantics:
#   - Dependabot / CodeQL: Critical or High severity alerts fail the gate.
#     Medium/Low are logged, not blocking.
#   - govulncheck has no per-finding severity field. A finding whose call
#     trace bottoms out in a third-party module is treated as blocking (it
#     means shipped code actually calls a known-vulnerable function — the
#     same class of thing Dependabot alerts on). A finding confined to
#     `stdlib` is a Go-toolchain patch-level issue, not a code change here —
#     logged prominently as an upgrade recommendation, not blocking.
#   - gosec: High-severity + High-confidence findings fail the gate.
#     Everything else is logged for visibility, not blocking. Both answers
#     come out of ONE `-fmt=json` run per module (#1570) — see the gosec
#     block below and lib/gosec-report.sh for why there used to be two, and
#     what "the scan covered 0 files" is refused for.
#   - npm audit: gate on the High+Critical count in the JSON report's
#     `.metadata.vulnerabilities` block. A non-zero exit alone is ambiguous —
#     npm exits non-zero both when it finds advisories and when the audit
#     itself can't run (registry unreachable, TLS failure) — so a report with
#     no metadata (an `.error`/`.message` payload instead) is classified as
#     "could not run" and reported as such, not as a fabricated advisory. Per
#     the "can't run at all" policy below that is still a hard failure, just
#     an accurately-labelled one.
#
# A check that can't run at all — missing tool, `gh` auth/scope failure — is
# a hard failure, not a skip. A silently-skipped scan is indistinguishable
# from a clean one, which defeats the point of a gate.
#
# Usage: tools/security-scan.sh [--local] [--changed]
set -uo pipefail

# Resolve the script's own directory before cd'ing to the repo root, so the
# lib/ source below still works from any caller's working directory.
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT=$(git rev-parse --show-toplevel)
cd "$REPO_ROOT"

# Sourced unconditionally and fatally, the same shape as the --changed lib
# below: without it gosec_report_check is undefined, `case $?` would take the
# catch-all arm, and every module would be reported as a failed scan. Loud
# either way — but "cannot load the classifier" is the accurate sentence.
# shellcheck source=lib/gosec-report.sh
. "$SCRIPT_DIR/lib/gosec-report.sh" || {
  echo "FAIL: cannot load $SCRIPT_DIR/lib/gosec-report.sh — refusing to classify a gosec report blind" >&2
  exit 2
}

LOCAL_ONLY=0
CHANGED=0
for arg in "$@"; do
  case "$arg" in
    --local) LOCAL_ONLY=1 ;;
    --changed) CHANGED=1 ;;
    # Print the whole leading comment block — no line numbers to drift out of
    # date as this header grows.
    -h|--help) awk 'NR>1 && /^#/ {print; next} NR>1 {exit}' "$0"; exit 0 ;;
    *) echo "unknown arg: $arg" >&2; exit 2 ;;
  esac
done

# Every module in go.work — govulncheck/gosec run per module, matching how
# go.work itself scopes builds (see tools/preflight.sh's `go` group for the
# narrower go-test set: only core, onboarding-factory, and starhistory have
# test suites today, but a vuln/SAST scan doesn't need tests to pass, so all
# six run).
#
# Read from go.work rather than restated here (#1291). A hand-maintained copy
# drifts silently in the one direction that matters: add a seventh module and
# forget this line, and that module is never scanned — it builds, it tests, and
# nothing says otherwise. `go work edit -json` is Go's own parse of the file,
# so this cannot disagree with what the toolchain builds.
GO_MODULES=()
while IFS= read -r mod; do
  [[ -n "$mod" ]] && GO_MODULES+=("$mod")
done < <(go work edit -json 2>/dev/null | jq -r '.Use[].DiskPath | sub("^\\./"; "")')
# An empty list here would sail through every loop below and report a pass
# having scanned nothing — the "silently-skipped scan is indistinguishable from
# a clean one" failure this file's header rules out. A parse that yields no
# modules means go/jq/go.work is broken, not that the repo has no Go code.
if [[ ${#GO_MODULES[@]} -eq 0 ]]; then
  echo "FAIL: could not read the module list from go.work (go work edit -json | jq) — refusing to report a pass for a scan that would cover nothing" >&2
  exit 2
fi

# No equivalent source of truth exists for the npm trees — nothing in the repo
# enumerates them the way go.work enumerates modules — so this list stays
# hand-maintained. tools/lib/module-list_test.sh holds both it and GO_MODULES
# against .github/dependabot.yml.
WEB_TREES=(platforms/web tools/onboarding-factory/internal/viewer/web)

FAILED=0
fail() { local msg="$1"; echo "FAIL: $msg" >&2; FAILED=1; return 0; }
warn() { local msg="$1"; echo "WARN: $msg" >&2; return 0; }
ok()   { local msg="$1"; echo "OK: $msg"; return 0; }

# ---- --changed scoping -----------------------------------------------------
# CHANGED_FILES stays empty without --changed, and the in_scope helpers below
# short-circuit to true — so the unscoped run is byte-for-byte what it was.
SKIPPED=0
# skip <what> <why> — a skipped scan must be as loud as a failed one. A
# silently-skipped scan is indistinguishable from a clean one, which is the
# same reasoning that makes a can't-run check a hard failure above; the
# difference is that this skip is a deliberate, stated narrowing rather than
# an unknown.
skip() { local what="$1" why="$2"; echo "SKIP: $what — $why"; SKIPPED=$((SKIPPED + 1)); return 0; }

# Narrow GO_MODULES/WEB_TREES in place, once, before anything reads them.
# Doing it here rather than inside each scanner loop matters: the scanners'
# shared preconditions below — installing govulncheck, hard-failing when gosec
# is absent — would otherwise still run with nothing in scope, so a push
# touching no Go file could be rejected for a missing gosec. That is #1213's
# exact shape (a push blocked by a pre-existing condition it cannot have
# caused), and guarding only the loop bodies would have left it alive.
if [[ "$CHANGED" -eq 1 ]]; then
  # One `source` covers missing, unreadable and malformed: under `set -u`
  # without `-e` a failed source would otherwise leave the predicates
  # undefined, every module and tree would fall out of scope, and the run
  # would exit 0 reporting "passed" — a clean bill of health from a scan that
  # never happened, the one outcome this file's header rules out.
  # shellcheck source=lib/changed-files.sh
  . "$SCRIPT_DIR/lib/changed-files.sh" || {
    echo "FAIL: --changed: cannot load $SCRIPT_DIR/lib/changed-files.sh — refusing to report a pass for a scan that never ran" >&2
    exit 2
  }
  CHANGED_FILES=$(changed_files_vs_origin_main) || exit 2

  echo "-- scope: --changed — scanning only what this branch's diff vs origin/main feeds --"
  all_modules=("${GO_MODULES[@]}"); GO_MODULES=()
  for mod in "${all_modules[@]}"; do
    if go_module_touched "$mod" "$CHANGED_FILES"; then
      GO_MODULES+=("$mod")
    else
      skip "govulncheck + gosec: $mod" "no .go/go.mod/go.sum change under it"
    fi
  done
  all_trees=("${WEB_TREES[@]}"); WEB_TREES=()
  for tree in "${all_trees[@]}"; do
    if web_tree_touched "$tree" "$CHANGED_FILES"; then
      WEB_TREES+=("$tree")
    else
      skip "npm audit: $tree" "no package.json/package-lock.json change under it"
    fi
  done
fi

# ---- GitHub-native alerts (full mode only) --------------------------------

gh_alert_gate() {
  local label="$1" endpoint="$2" severity_filter="$3"
  local raw rc
  raw=$(gh api --method GET --paginate "$endpoint" -f state=open --jq '.[]' 2>&1)
  rc=$?
  if [[ $rc -ne 0 ]]; then
    fail "$label: \`gh api $endpoint\` failed — auth or missing scope? output: $raw"
    fail "$label: fix with: gh auth refresh -s security_events"
    return
  fi
  local hits count
  if [[ -z "$raw" ]]; then
    hits='[]'
  else
    hits=$(echo "$raw" | jq -s "[.[] | select($severity_filter)]")
  fi
  count=$(echo "$hits" | jq 'length')
  if [[ "$count" -gt 0 ]]; then
    fail "$label: $count open Critical/High alert(s)"
    echo "$hits" | jq -r '.[] | "  - " + (.html_url // (.number|tostring)) + " — " + (.security_advisory.summary // .rule.description // "no summary")' >&2
  else
    ok "$label: no open Critical/High alerts"
  fi
}

if [[ "$LOCAL_ONLY" -eq 0 ]]; then
  REPO_NWO=$(gh repo view --json nameWithOwner -q .nameWithOwner)
  echo "-- Dependabot alerts ($REPO_NWO) --"
  gh_alert_gate "dependabot" "repos/$REPO_NWO/dependabot/alerts" \
    '(.security_advisory.severity=="critical" or .security_advisory.severity=="high")'

  echo "-- CodeQL code-scanning alerts ($REPO_NWO) --"
  gh_alert_gate "codeql" "repos/$REPO_NWO/code-scanning/alerts" \
    '((.rule.security_severity_level=="critical" or .rule.security_severity_level=="high") or (.rule.security_severity_level==null and .rule.severity=="error"))'
fi

# ---- govulncheck (all modes) ----------------------------------------------
# The toolchain check lives inside the loop, matching the npm block below: with
# no module in scope it must not run at all, or a push touching no Go file pays
# a network install (and fails outright when `go` isn't on PATH) for a scan
# with nothing to scan — #1213's shape exactly.

for mod in "${GO_MODULES[@]+"${GO_MODULES[@]}"}"; do
  command -v govulncheck >/dev/null 2>&1 || go install golang.org/x/vuln/cmd/govulncheck@latest
  echo "-- govulncheck: $mod --"
  gv_json=$(mktemp)
  gv_err=$(mktemp)
  if ! ( cd "$mod" && govulncheck -format json ./... ) >"$gv_json" 2>"$gv_err"; then
    # govulncheck exits non-zero both when it finds vulnerabilities and when
    # it hits a real scan error (e.g. build failure) — the JSON body tells
    # them apart: a scan error has no "finding" entries at all.
    finding_count=$(jq -s '[.[] | select(has("finding"))] | length' "$gv_json" 2>/dev/null || echo 0)
    if [[ "$finding_count" -eq 0 ]]; then
      fail "govulncheck: $mod — scan itself failed (not a vulnerability finding): $(cat "$gv_err")"
      rm -f "$gv_json" "$gv_err"
      continue
    fi
  fi
  rm -f "$gv_err"
  # Trace[0] is the vulnerable symbol's own module. A missing/empty trace is
  # treated as non-stdlib (blocking) — fail safe rather than silently pass.
  third_party=$(jq -s '[.[] | select(has("finding")) | select((.finding.trace[0].module // "unknown") != "stdlib") | .finding.osv] | unique' "$gv_json")
  stdlib_only=$(jq -s '[.[] | select(has("finding")) | select((.finding.trace[0].module // "unknown") == "stdlib") | .finding.osv] | unique' "$gv_json")
  tp_count=$(echo "$third_party" | jq 'length')
  std_count=$(echo "$stdlib_only" | jq 'length')
  if [[ "$tp_count" -gt 0 ]]; then
    fail "govulncheck: $mod — $tp_count vulnerability/ies in called third-party code: $(echo "$third_party" | jq -c .)"
  fi
  if [[ "$std_count" -gt 0 ]]; then
    warn "govulncheck: $mod — $std_count Go-toolchain (stdlib) vulnerability/ies: $(echo "$stdlib_only" | jq -c .) — not blocking; fix by bumping the Go toolchain, not application code"
  fi
  if [[ "$tp_count" -eq 0 && "$std_count" -eq 0 ]]; then
    ok "govulncheck: $mod — clean"
  fi
  rm -f "$gv_json"
done

# ---- gosec (all modes) -----------------------------------------------------
# Same placement, and for a sharper reason: "gosec not found" is a hard
# failure, so hoisting it out of the loop would reject a push that had no Go
# module to scan — again #1213's shape.
#
# ONE run per module, not two (#1570). This used to be an unfiltered
# informational pass followed by a `-severity high -confidence high` gate
# pass — but those flags filter the REPORT, not the analysis, so the second
# run re-derived a strict subset of the first at full price: 172s + 172s on
# the core module, 344s of the security gate's measured 355s. The single JSON
# report carries both answers and lib/gosec-report.sh reads them out. Nothing
# is scanned less: still `./...`, still unfiltered.

for mod in "${GO_MODULES[@]+"${GO_MODULES[@]}"}"; do
  command -v gosec >/dev/null 2>&1 || { fail "gosec not found — install: go install github.com/securego/gosec/v2/cmd/gosec@latest"; continue; }
  echo "-- gosec: $mod (one pass; informational listing + High/High gate) --"
  gs_json=$(mktemp)
  gs_err=$(mktemp)
  # -no-fail so the exit status carries no verdict: the verdict is the report,
  # and a gosec that exits non-zero merely because it found a LOW issue must
  # not be confused with one that could not run. "Could not run" is caught by
  # gosec_report_check, which refuses an unparseable or zero-file report
  # rather than reading it as clean.
  ( cd "$mod" && gosec -no-fail -quiet -fmt=json -out="$gs_json" ./... ) >/dev/null 2>"$gs_err"
  gosec_report_check "$gs_json" "$mod"
  case $? in
    0) ok "gosec: $mod — no High/High findings" ;;
    1) fail "gosec: $mod — High-severity/High-confidence finding(s) — listed above" ;;
    *) fail "gosec: $mod — the scan did not produce a readable report; this is NOT a clean result: $(tr '\n' ' ' <"$gs_err" | sed 's/  */ /g' | cut -c1-400)" ;;
  esac
  rm -f "$gs_json" "$gs_err"
done

# ---- npm audit (all modes) -------------------------------------------------
# `command -v npm` already sits inside the loop, so an out-of-scope tree never
# reaches it — the shape the two Go blocks above were corrected to match.

for tree in "${WEB_TREES[@]+"${WEB_TREES[@]}"}"; do
  echo "-- npm audit: $tree --"
  command -v npm >/dev/null 2>&1 || { fail "npm audit: $tree — npm not found"; continue; }
  # npm audit exits non-zero both when it finds advisories AND when the audit
  # can't run at all (registry unreachable, TLS failure) — so the exit code
  # alone can't tell the two apart. Parse the JSON report and branch on its
  # shape, the same way the govulncheck block distinguishes a real finding
  # from a scan error: a completed audit carries a `.metadata.vulnerabilities`
  # block; a failed audit has no metadata and instead carries an
  # `.error`/`.message` payload.
  na_err=$(mktemp)
  na_json=$( cd "$tree" && npm audit --json 2>"$na_err" )
  if echo "$na_json" | jq -e '.metadata.vulnerabilities' >/dev/null 2>&1; then
    hc=$(echo "$na_json" | jq '(.metadata.vulnerabilities.high // 0) + (.metadata.vulnerabilities.critical // 0)')
    if [[ "$hc" -gt 0 ]]; then
      fail "npm audit: $tree — $hc High/Critical advisory/ies found; fix with npm audit fix, or document a suppression"
      echo "$na_json" | jq -r '.vulnerabilities // {} | to_entries[] | select(.value.severity=="high" or .value.severity=="critical") | "  - " + .key + " (" + .value.severity + ")"' >&2
    else
      ok "npm audit: $tree — no High/Critical advisories"
    fi
  else
    # No metadata block: the audit never completed. A silently-skipped scan is
    # indistinguishable from a clean one, so this is a hard failure — but named
    # for the real cause rather than misreported as an advisory. Prefer the
    # JSON error payload; fall back to npm's stderr when the report is empty
    # (e.g. the tree is missing or npm errored before emitting JSON).
    na_msg=$(echo "$na_json" | jq -r '.message // .error.summary // .error.detail // empty' 2>/dev/null)
    [[ -z "$na_msg" ]] && na_msg=$(tr '\n' ' ' <"$na_err" | sed 's/  */ /g; s/^ *//; s/ *$//')
    fail "npm audit: $tree — audit could not run (could not reach the npm registry, or the audit errored): ${na_msg:-no detail available} — re-run; this is NOT a vulnerability finding"
  fi
  rm -f "$na_err"
done

echo
# Name the narrowing in the verdict too. "passed" after skipping most of the
# scan means something weaker than an unscoped "passed", and the line a
# developer actually reads should say so rather than implying full coverage.
scope_note=""
[[ "$SKIPPED" -gt 0 ]] && scope_note=" ($SKIPPED scan(s) skipped as out of scope for this diff — run tools/security-scan.sh without --changed for the full sweep)"
if [[ "$FAILED" -eq 1 ]]; then
  echo "security-scan: FAILED — resolve the Critical/High finding(s) above before shipping.$scope_note" >&2
  exit 1
fi
echo "security-scan: passed$scope_note"
