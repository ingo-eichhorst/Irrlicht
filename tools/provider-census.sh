#!/usr/bin/env bash
# provider-census.sh — the re-runnable inventory command for the billing-
# product catalog (issue #2006, part of epic #1977's provider-attribution
# track).
#
# WHY THIS EXISTS. #1977 §5: "Import their complete relevant provider lists
# with the source revision and a reproducible inventory command. Do not
# maintain an unexplained provider count by hand." A number a person types
# once into a doc drifts away from what it measured the moment anyone edits
# the underlying list. This command is the thing that measures it, every
# time, from the same committed data docs/providers/catalog.md cites.
#
# WHAT IT READS. Every docs/providers/sources/*.json file — one per pinned
# OSS source from #2006's spec. Each file records: which repo and revision
# it was read at, which paths were inspected, its licence at that revision,
# a `kind` ("provider-list" sources contribute to the total; "reference"
# sources — herdr-agent-usage's agent-identity/billing-mode split — do not,
# and are reported separately), and the `entries` array itself.
#
# WHAT IT DOES NOT DO. No credential read, no outbound request to any
# provider, no parser for provider responses. The only network calls this
# script makes (and only outside --offline) are read-only GET requests to
# each source's own `check_url` (a GitHub commits-API URL), to confirm the
# pinned revision this repo's catalog cites still resolves.
#
# THE LOUD-FAILURE RULE (issue #2006 §7 — "the check must fail loudly when
# it cannot look"; "an empty list must never render as no providers found").
# Two DISTINCT failure paths implement this, and they are kept distinct
# rather than folded into one "something's wrong" exit code, because a
# reviewer or a later caller needs to tell them apart:
#   - exit 3: a "provider-list" source's `entries` array is empty, or a
#     source file is missing a required field. This is a CONTENT failure —
#     the imported list itself is broken or was never populated — and it is
#     reported by source id, never silently treated as "this source
#     contributed 0".
#   - exit 4: a source's pinned revision did not answer at its `check_url`
#     (network failure, or the revision no longer resolves upstream). This
#     is a REACHABILITY failure, reported by source id and revision, and it
#     also aborts BEFORE printing a total — a run that could not confirm
#     every source is pinned data still trusts must not print a number that
#     looks complete.
# Both cases exit non-zero and print which source and why; see
# tools/lib/provider-census_test.sh for the mutation fixtures proving each
# one goes red on its own trigger and does not fire on the other's.
#
# Usage:
#   tools/provider-census.sh [--dir <sources-dir>] [--offline] [--source <id>] [--markdown]
#
#   --dir <sources-dir>  directory of *.json source files (default:
#                        docs/providers/sources, resolved against the repo
#                        root so this runs the same from any cwd).
#   --offline            skip the network reachability check (--check_url).
#                        Used by the hermetic test fixtures and by anyone
#                        without network access; the printed total still
#                        comes from the committed entries, just without the
#                        "still resolves upstream" guarantee.
#   --source <id>        restrict every action to one source id.
#   --markdown           emit one markdown table row per entry (id, source,
#                        kind, product) instead of the summary counts —
#                        this is what feeds docs/providers/catalog.md's
#                        per-candidate table, generated rather than typed.
#
# Exit codes:
#   0  every source validated (and, unless --offline, every source's pinned
#      revision answered at its check_url); the printed total is
#      trustworthy.
#   1  usage error (bad flag, or --source names a file that is not there).
#   2  no sources found at all — the corpus itself is empty or the
#      directory is missing. An empty CORPUS is a refusal, distinct from
#      case 3's empty ENTRIES LIST inside one otherwise-present source.
#   3  schema or content failure: a required field is missing, or a
#      "provider-list" source's entries array is empty. Named by source id.
#   4  a source's pinned revision is unreachable. Named by source id and
#      revision.
set -uo pipefail

REPO_ROOT=$(git rev-parse --show-toplevel 2>/dev/null) || {
  echo "provider-census: not inside a git repository" >&2
  exit 1
}

SOURCES_DIR="$REPO_ROOT/docs/providers/sources"
OFFLINE=0
ONLY_SOURCE=""
MARKDOWN=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dir)
      [[ $# -ge 2 ]] || { echo "provider-census: --dir needs a value" >&2; exit 1; }
      SOURCES_DIR="$2"; shift 2 ;;
    --offline)
      OFFLINE=1; shift ;;
    --source)
      [[ $# -ge 2 ]] || { echo "provider-census: --source needs a value" >&2; exit 1; }
      ONLY_SOURCE="$2"; shift 2 ;;
    --markdown)
      MARKDOWN=1; shift ;;
    -h|--help)
      sed -n '2,70p' "$0"; exit 0 ;;
    *)
      echo "provider-census: unrecognized argument '$1'" >&2; exit 1 ;;
  esac
done

command -v jq >/dev/null 2>&1 || {
  echo "provider-census: jq is required and was not found on PATH" >&2
  exit 1
}

if [[ ! -d "$SOURCES_DIR" ]]; then
  echo "provider-census: REFUSING — sources directory '$SOURCES_DIR' does not exist" >&2
  echo "provider-census: an empty or missing corpus is a refusal, not a run that found 0 providers" >&2
  exit 2
fi

FILES=()
while IFS= read -r -d '' f; do FILES+=("$f"); done \
  < <(find "$SOURCES_DIR" -maxdepth 1 -type f -name '*.json' -print0 | sort -z)

if [[ -n "$ONLY_SOURCE" ]]; then
  match=""
  for f in "${FILES[@]}"; do
    [[ "$(basename "$f" .json)" == "$ONLY_SOURCE" ]] && match="$f"
  done
  if [[ -z "$match" ]]; then
    echo "provider-census: REFUSING — no source file for id '$ONLY_SOURCE' under $SOURCES_DIR" >&2
    exit 1
  fi
  FILES=("$match")
fi

if [[ ${#FILES[@]} -eq 0 ]]; then
  echo "provider-census: REFUSING — no *.json source files under $SOURCES_DIR" >&2
  echo "provider-census: an empty corpus is a refusal, not a run that found 0 providers" >&2
  exit 2
fi

# ---- pass 1: schema + content validation (no network) ---------------------
declare -a IDS=() KINDS=() COUNTS=() REVISIONS=() CHECK_URLS=() NAMES=()
rc=0
for f in "${FILES[@]}"; do
  bad_field=""
  for field in id name repo revision kind check_url license evidence_date entries; do
    has=$(jq -r --arg f "$field" 'has($f)' "$f" 2>/dev/null) || { bad_field="<unreadable JSON>"; break; }
    if [[ "$has" != "true" ]]; then bad_field="$field"; break; fi
  done
  if [[ -n "$bad_field" ]]; then
    echo "provider-census: SCHEMA FAILURE in $(basename "$f") — missing or unreadable field '$bad_field'" >&2
    rc=3
    continue
  fi

  id=$(jq -r '.id' "$f")
  name=$(jq -r '.name' "$f")
  kind=$(jq -r '.kind' "$f")
  revision=$(jq -r '.revision' "$f")
  check_url=$(jq -r '.check_url' "$f")
  count=$(jq -r '.entries | length' "$f")

  if [[ "$kind" != "provider-list" && "$kind" != "reference" ]]; then
    echo "provider-census: SCHEMA FAILURE in $(basename "$f") — kind '$kind' is neither 'provider-list' nor 'reference'" >&2
    rc=3
    continue
  fi

  if [[ "$kind" == "provider-list" && "$count" -eq 0 ]]; then
    echo "provider-census: CONTENT FAILURE — source '$id' ($(basename "$f")) is kind=provider-list with an EMPTY entries list." >&2
    echo "provider-census: this is refused, not reported as '$id contributed 0 providers' — an empty imported list is a broken import, not a finding." >&2
    rc=3
    continue
  fi

  IDS+=("$id"); NAMES+=("$name"); KINDS+=("$kind"); COUNTS+=("$count")
  REVISIONS+=("$revision"); CHECK_URLS+=("$check_url")
done

if [[ $rc -ne 0 ]]; then
  echo "provider-census: schema/content validation failed for one or more sources; see above. No total printed." >&2
  exit "$rc"
fi

# ---- pass 2: reachability (network, skipped under --offline) --------------
if [[ "$OFFLINE" -eq 0 ]]; then
  unreachable=0
  for i in "${!IDS[@]}"; do
    id="${IDS[$i]}"; revision="${REVISIONS[$i]}"; url="${CHECK_URLS[$i]}"
    if ! curl -fsS --max-time 10 -o /dev/null "$url"; then
      echo "provider-census: UNREACHABLE — source '$id' pinned revision '$revision' did not answer at $url" >&2
      unreachable=1
    fi
  done
  if [[ "$unreachable" -eq 1 ]]; then
    echo "provider-census: one or more pinned revisions are unreachable; refusing to print a total that could be stale or wrong." >&2
    echo "provider-census: re-run with --offline to compute from the locally recorded entries without this guarantee." >&2
    exit 4
  fi
fi

# ---- output -----------------------------------------------------------
if [[ "$MARKDOWN" -eq 1 ]]; then
  echo "| product | source | kind | revision | evidence date | state | next research action |"
  echo "|---|---|---|---|---|---|---|"
  for i in "${!IDS[@]}"; do
    f=""
    for cf in "${FILES[@]}"; do [[ "$(basename "$cf" .json)" == "${IDS[$i]}" ]] && f="$cf"; done
    jq -r --arg src "${IDS[$i]}" --arg kind "${KINDS[$i]}" --arg rev "${REVISIONS[$i]}" '
      .evidence_date as $date
      | (.inspected_paths | join("; ")) as $paths
      | .repo as $repo
      | if $kind == "provider-list" then
          "unassessed" as $state
          | ("Fixture-verify against `\($repo)`@`\($rev[0:12])`: \($paths)") as $next
          | .entries[] | "| \(.name) | \($src) | \($kind) | \($rev[0:12]) | \($date) | \($state) | \($next) |"
        else
          .entries[] | "| \(.name) | \($src) | \($kind) | \($rev[0:12]) | \($date) | n/a (reference, not a billing candidate) | n/a — see \($src)'\''s notes field |"
        end
    ' "$f"
  done
  exit 0
fi

total=0
echo "provider-census: sources read from $SOURCES_DIR"
[[ "$OFFLINE" -eq 1 ]] && echo "provider-census: --offline — pinned-revision reachability was NOT re-checked this run"
for i in "${!IDS[@]}"; do
  if [[ "${KINDS[$i]}" == "provider-list" ]]; then
    printf 'provider-census:   %-20s provider-list  entries=%-4s (counted)\n' "${IDS[$i]}" "${COUNTS[$i]}"
    total=$((total + COUNTS[i]))
  else
    printf 'provider-census:   %-20s reference      entries=%-4s (excluded from total — see notes)\n' "${IDS[$i]}" "${COUNTS[$i]}"
  fi
done
echo "provider-census: TOTAL candidate billing-product mentions across provider-list sources: $total"
exit 0
