#!/usr/bin/env bash
# site-lint.sh — mechanical checks over site/, the static pages published to
# irrlicht.io. Until issue #1894 no gate read them at all: a page could link to
# a file that does not exist, leave a tag unclosed, or drop out of the sitemap,
# and every CI check stayed green because none of them opened site/.
#
# The checks are the ones a machine can decide without taste:
#
#   1. dead-local-link   ERROR  href/src pointing at a file that is not there
#   2. unclosed-tag      ERROR  a block element opened and never closed
#   3. missing-head      ERROR  a page with no <title> or no canonical link
#   4. sitemap-missing   WARN   a docs page absent from site/sitemap.xml
#   5. sitemap-dangling  ERROR  a sitemap URL naming no file in site/
#
# Wording, layout and accessibility stay a human judgement call.
#
# Exit status is 1 when something failed at the severity in force. --strict
# promotes warnings to failures.
set -euo pipefail

SITE_DIR="${SITE_DIR:-site}"
STRICT=0
FAILED=0
WARNED=0
# CHECKED counts the files this run actually opened. A linter that silently
# looks at nothing passes; this is what turns that into a loud failure.
CHECKED=0

usage() {
  cat <<'USAGE'
usage: site-lint.sh [--strict] [--site DIR]

  --strict    treat warnings as failures
  --site DIR  lint DIR instead of ./site (also via SITE_DIR)
USAGE
}

while [ $# -gt 0 ]; do
  case "$1" in
    --strict) STRICT=1; shift ;;
    --site) SITE_DIR="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) printf 'site-lint: unknown argument %s\n' "$1" >&2; usage >&2; exit 2 ;;
  esac
done

err() { printf 'ERROR  %s: %s\n' "$1" "$2" >&2; FAILED=$((FAILED + 1)); }
warn() {
  printf 'WARN   %s: %s\n' "$1" "$2" >&2
  WARNED=$((WARNED + 1))
  [ "$STRICT" -eq 1 ] && FAILED=$((FAILED + 1))
  return 0
}

if [ ! -d "$SITE_DIR" ]; then
  printf 'site-lint: %s is not a directory — this lint cannot run\n' "$SITE_DIR" >&2
  exit 2
fi

# ---------------------------------------------------------------- 1, 2, 3
# One pass per page. Every href/src that is not absolute, not a fragment and
# not a mailto:/tel: must resolve to a file on disk.
lint_page() {
  page="$1"
  dir="$(dirname "$page")"
  CHECKED=$((CHECKED + 1))

  # 3 — head essentials.
  grep -q '<title>' "$page" || err "$page" "no <title>"
  grep -q 'rel="canonical"' "$page" || err "$page" 'no rel="canonical" link'

  # 1 — local links.
  # `|| true` on every grep that may legitimately match nothing: under
  # `set -euo pipefail` a no-match exit 1 would kill this script mid-run, and a
  # linter that dies silently reports exactly what a clean site reports.
  { grep -oE '(href|src)="[^"#][^"]*"' "$page" || true; } \
    | sed -E 's/^(href|src)="//; s/"$//' \
    | while IFS= read -r target; do
        case "$target" in
          http://*|https://*|//*|mailto:*|tel:*|data:*|'#'*) continue ;;
        esac
        clean="${target%%\#*}"
        clean="${clean%%\?*}"
        [ -z "$clean" ] && continue
        case "$clean" in
          /*) resolved="$SITE_DIR$clean" ;;
          *)  resolved="$dir/$clean" ;;
        esac
        if [ ! -e "$resolved" ]; then
          printf 'DEAD\t%s\t%s\n' "$page" "$target"
        fi
      done

  # 2 — block tags opened and never closed. Counted per tag name, self-closing
  # and void elements excluded by construction because none is listed here.
  for tag in html head body main nav table thead tbody tr td th ul ol li section article; do
    opened=$({ grep -oE "<$tag(>| )" "$page" || true; } | wc -l | tr -d ' ')
    closed=$({ grep -oE "</$tag>" "$page" || true; } | wc -l | tr -d ' ')
    if [ "$opened" != "$closed" ]; then
      printf 'UNBALANCED\t%s\t<%s> opened %s time(s), closed %s\n' "$page" "$tag" "$opened" "$closed"
    fi
  done
}

findings="$(mktemp)"
trap 'rm -f "$findings"' EXIT

pages=$(find "$SITE_DIR" -name '*.html' -type f | sort || true)
if [ -z "$pages" ]; then
  printf 'site-lint: no HTML page found under %s — this lint cannot run\n' "$SITE_DIR" >&2
  exit 2
fi

for page in $pages; do
  lint_page "$page" >>"$findings"
done

while IFS="$(printf '\t')" read -r kind page detail extra; do
  case "$kind" in
    DEAD) err "$page" "dead local link: $detail" ;;
    UNBALANCED) err "$page" "$detail $extra" ;;
  esac
done <"$findings"

# ---------------------------------------------------------------- 4, 5
SITEMAP="$SITE_DIR/sitemap.xml"
if [ -f "$SITEMAP" ]; then
  CHECKED=$((CHECKED + 1))
  # 5 — every sitemap URL must name a file that exists.
  { grep -oE '<loc>[^<]+</loc>' "$SITEMAP" || true; } \
    | sed -E 's#</?loc>##g; s#^https?://[^/]+##' \
    | while IFS= read -r path; do
        case "$path" in
          ''|'/') candidate="$SITE_DIR/index.html" ;;
          */) candidate="$SITE_DIR${path}index.html" ;;
          *)  candidate="$SITE_DIR$path" ;;
        esac
        [ -e "$candidate" ] || printf 'DANGLING\t%s\n' "$path"
      done >"$findings"
  while IFS="$(printf '\t')" read -r kind path; do
    [ "$kind" = DANGLING ] && err "$SITEMAP" "names $path, which is not in $SITE_DIR"
  done <"$findings"

  # 4 — every docs page should be listed.
  for page in $(find "$SITE_DIR/docs" -name '*.html' -type f 2>/dev/null | sort || true); do
    rel="${page#"$SITE_DIR"}"
    base="$(basename "$rel")"
    if [ "$base" = index.html ]; then
      grep -q "docs/</loc>\|docs/index.html</loc>" "$SITEMAP" || warn "$SITEMAP" "does not list $rel"
      continue
    fi
    grep -q "$rel</loc>" "$SITEMAP" || warn "$SITEMAP" "does not list $rel"
  done
else
  warn "$SITE_DIR" "has no sitemap.xml"
fi

# A run that opened nothing has not checked a site. Absence of a finding and
# inability to look must never produce the same output.
if [ "$CHECKED" -eq 0 ]; then
  printf 'site-lint: opened no file under %s — this lint cannot run\n' "$SITE_DIR" >&2
  exit 2
fi

if [ "$FAILED" -gt 0 ]; then
  printf 'site-lint: %d failure(s), %d warning(s); read %d file(s) under %s\n' \
    "$FAILED" "$WARNED" "$CHECKED" "$SITE_DIR" >&2
  exit 1
fi
printf 'OK: site-lint — read %d file(s) under %s; %d warning(s)\n' "$CHECKED" "$SITE_DIR" "$WARNED"
