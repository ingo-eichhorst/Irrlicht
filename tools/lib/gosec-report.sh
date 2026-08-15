#!/usr/bin/env bash
# gosec-report.sh — read ONE gosec JSON report and answer both questions
# tools/security-scan.sh asks of it: what did it find (informational, every
# severity), and does anything in it block the gate (High severity AND High
# confidence). Sourced, never executed.
#
#   . "$SCRIPT_DIR/lib/gosec-report.sh"
#   gosec -no-fail -quiet -fmt=json -out="$json" ./...
#   gosec_report_check "$json" "core"
#
# ---------------------------------------------------------------------------
# Why this exists (#1570)
#
# security-scan.sh used to run gosec TWICE per module over the same tree:
#
#     gosec -no-fail -quiet ./...                          # informational
#     gosec -quiet -severity high -confidence high ./...   # the gate
#
# gosec's -severity/-confidence are report filters applied after the analysis,
# not analysis switches, so the second run re-derived a strict subset of the
# first at full price. Measured on the core module (263 files, 66,534 lines):
# 172s + 172s. The whole security gate on a one-file core/ diff was 355s, of
# which 344s was gosec analysing the same code twice.
#
# One `-fmt=json` run costs 174s and carries both answers — .Stats for the
# informational summary, .Issues[].severity/.confidence for the gate. Nothing
# is scanned less: the module still gets `./...`, unfiltered. This is the
# reason the security gate was NOT narrowed to changed packages instead, which
# would have been a real reduction in what gets scanned; halving a duplicate
# is not.
#
# ---------------------------------------------------------------------------
# Why an unreadable report is a hard failure, and why an EMPTY one is too
#
# security-scan.sh's header: "A silently-skipped scan is indistinguishable
# from a clean one." A report that will not parse, and a report whose own
# .Stats says it read zero files, both produce "no High/High findings" if you
# only count matching issues — a perfect clean bill of health from a scan that
# never looked at anything. Both are refused here with status 2.

# gosec_report_check <json-file> [label] — classify one report and print its
# findings. Returns:
#   0 — readable, covered at least one file, no High/High finding
#   1 — readable, and at least one High/High finding (listed on stderr)
#   2 — unreadable, or covered no file at all (the scan did not happen)
gosec_report_check() {
  local json="${1:-}" label="${2:-gosec}"

  if [[ -z "$json" || ! -s "$json" ]]; then
    echo "gosec-report: $label — no report at '${json:-<none>}' (gosec produced nothing to read)" >&2
    return 2
  fi
  if ! jq -e . "$json" >/dev/null 2>&1; then
    echo "gosec-report: $label — report at '$json' is not valid JSON (gosec did not finish)" >&2
    return 2
  fi

  local files lines found
  files=$(jq -r '.Stats.files // "null"' "$json" 2>/dev/null)
  lines=$(jq -r '.Stats.lines // 0' "$json" 2>/dev/null)
  found=$(jq -r '.Stats.found // 0' "$json" 2>/dev/null)
  case "$files" in
    '' | null | *[!0-9]*)
      echo "gosec-report: $label — report at '$json' carries no .Stats.files count; refusing to read it as a clean scan" >&2
      return 2
      ;;
  esac
  if [[ "$files" -eq 0 ]]; then
    echo "gosec-report: $label — the scan covered 0 files. A scan that read nothing is not a clean scan." >&2
    return 2
  fi

  echo "   scanned $files file(s) / $lines line(s); $found issue(s) at all severities"
  # The whole finding list, one line each. The two-run version printed gosec's
  # own text report — every issue plus three lines of surrounding code — so
  # this is the same information an order of magnitude shorter, not less of it.
  jq -r '(.Issues // [])[] | "   \(.severity)/\(.confidence) \(.rule_id) \(.file):\(.line) — \(.details)"' "$json"

  local blocking
  blocking=$(jq -r '[(.Issues // [])[] | select(.severity == "HIGH" and .confidence == "HIGH")] | length' "$json" 2>/dev/null)
  case "$blocking" in
    '' | *[!0-9]*)
      echo "gosec-report: $label — could not count High/High findings in '$json'" >&2
      return 2
      ;;
  esac
  if [[ "$blocking" -gt 0 ]]; then
    jq -r '(.Issues // [])[] | select(.severity == "HIGH" and .confidence == "HIGH") | "  - \(.rule_id) \(.file):\(.line) — \(.details)"' "$json" >&2
    return 1
  fi
  return 0
}
