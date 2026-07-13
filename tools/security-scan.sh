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
#     Everything else is logged for visibility, not blocking.
#   - npm audit --audit-level=high already implements this split natively
#     (fails only on high/critical advisories).
#
# A check that can't run at all — missing tool, `gh` auth/scope failure — is
# a hard failure, not a skip. A silently-skipped scan is indistinguishable
# from a clean one, which defeats the point of a gate.
#
# Usage: tools/security-scan.sh [--local]
set -uo pipefail

REPO_ROOT=$(git rev-parse --show-toplevel)
cd "$REPO_ROOT"

LOCAL_ONLY=0
for arg in "$@"; do
  case "$arg" in
    --local) LOCAL_ONLY=1 ;;
    -h|--help) sed -n '2,30p' "$0"; exit 0 ;;
    *) echo "unknown arg: $arg" >&2; exit 2 ;;
  esac
done

# Every module in go.work — govulncheck/gosec run per module, matching how
# go.work itself scopes builds (see tools/preflight.sh's `go` group for the
# narrower go-test set: only core, onboarding-factory, and starhistory have
# test suites today, but a vuln/SAST scan doesn't need tests to pass, so all
# six run).
GO_MODULES=(core tools/onboarding-factory tools/wsload tools/seed-demo-sessions tools/eta-research tools/starhistory)
WEB_TREES=(platforms/web tools/onboarding-factory/internal/viewer/web)

FAILED=0
fail() { local msg="$1"; echo "FAIL: $msg" >&2; FAILED=1; return 0; }
warn() { local msg="$1"; echo "WARN: $msg" >&2; return 0; }
ok()   { local msg="$1"; echo "OK: $msg"; return 0; }

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

command -v govulncheck >/dev/null 2>&1 || go install golang.org/x/vuln/cmd/govulncheck@latest

for mod in "${GO_MODULES[@]}"; do
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

command -v gosec >/dev/null 2>&1 || fail "gosec not found — install: go install github.com/securego/gosec/v2/cmd/gosec@latest"

if command -v gosec >/dev/null 2>&1; then
  for mod in "${GO_MODULES[@]}"; do
    echo "-- gosec: $mod (informational, all severities) --"
    ( cd "$mod" && gosec -no-fail -quiet ./... ) || true

    echo "-- gosec: $mod (gate: High severity + High confidence) --"
    if ( cd "$mod" && gosec -quiet -severity high -confidence high ./... ); then
      ok "gosec: $mod — no High/High findings"
    else
      fail "gosec: $mod — High-severity/High-confidence finding(s) — see output above"
    fi
  done
fi

# ---- npm audit (all modes) -------------------------------------------------

for tree in "${WEB_TREES[@]}"; do
  echo "-- npm audit: $tree --"
  command -v npm >/dev/null 2>&1 || { fail "npm audit: $tree — npm not found"; continue; }
  if ( cd "$tree" && npm audit --audit-level=high ); then
    ok "npm audit: $tree — no High/Critical advisories"
  else
    fail "npm audit: $tree — High/Critical advisory/ies found (see above); fix with npm audit fix, or document a suppression"
  fi
done

echo
if [[ "$FAILED" -eq 1 ]]; then
  echo "security-scan: FAILED — resolve the Critical/High finding(s) above before shipping." >&2
  exit 1
fi
echo "security-scan: passed"
