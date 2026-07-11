#!/usr/bin/env bash
# dora-metrics.sh — directional DORA metrics (Deployment Frequency, Lead
# Time for Changes) computed entirely from local git tags/history, plus
# gh release list as an optional enrichment for release-published
# timestamps. No dashboard, no persistence — just a plain-text summary.
#
# Change Failure Rate and Time to Restore Service (MTTR) are NOT computed
# here — see #936 for that phase; it needs a decision on what counts as a
# "failure" (hotfix within N hours? revert commit? bug-labeled issue linked
# to a release?) before it can be built.
#
# Assumes release tags are named v<major>.<minor>.<patch> (matches
# version.json's "version" field) and are created on main's ancestry — no
# defensive merge-base/is-ancestor check is done in this version.
#
# Portable to macOS bash 3.2 — avoid mapfile/readarray and negative array
# indices, use while-read loops (see tools/replay-fixtures.sh for the same
# convention).
#
# Usage:
#   tools/dora-metrics.sh                              # last 90 days
#   tools/dora-metrics.sh --since "30 days ago"
#   tools/dora-metrics.sh --since 2026-01-01 --until 2026-04-01
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

SINCE="90 days ago"
UNTIL="now"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --since) SINCE="$2"; shift 2 ;;
    --until) UNTIL="$2"; shift 2 ;;
    -h|--help) sed -n '2,22p' "$0"; exit 0 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done

# Portable date parsing: GNU `date -d` handles any free-form spec; BSD/macOS
# date has no -d, so fall back to its two supported shapes here — an ISO
# date (-j -f "%Y-%m-%d") and "<N> days ago" (-v-<N>d).
to_epoch() {
  local spec="$1"
  if date -d "$spec" +%s 2>/dev/null; then
    return 0
  fi
  # BSD `date -j -f "%Y-%m-%d"` fills unspecified hour/min/sec from the
  # current time rather than midnight — pin to 00:00:00 explicitly.
  if date -j -f "%Y-%m-%d %H:%M:%S" "$spec 00:00:00" +%s 2>/dev/null; then
    return 0
  fi
  if [[ "$spec" =~ ^([0-9]+)\ days?\ ago$ ]]; then
    date -v-"${BASH_REMATCH[1]}"d +%s 2>/dev/null && return 0
  fi
  return 1
}

SINCE_EPOCH=$(to_epoch "$SINCE") || { echo "ERROR: could not parse --since '$SINCE'" >&2; exit 2; }
if [[ "$UNTIL" == "now" ]]; then
  UNTIL_EPOCH=$(date +%s)
else
  UNTIL_EPOCH=$(to_epoch "$UNTIL") || { echo "ERROR: could not parse --until '$UNTIL'" >&2; exit 2; }
fi

if [[ "$(git rev-parse --is-shallow-repository 2>/dev/null || echo false)" == "true" ]]; then
  echo "WARN: shallow clone detected — metrics may undercount; run \`git fetch --unshallow\`" >&2
fi

TAG_PATTERN='^v[0-9]+\.[0-9]+\.[0-9]+$'

# Parallel arrays of tag<TAB>epoch, oldest first, filtered to real release
# tags. Avoid mapfile for bash 3.2 compat.
TAG_NAMES=()
TAG_EPOCHS=()
while IFS=$'\t' read -r name epoch; do
  [[ -z "$name" ]] && continue
  if [[ "$name" =~ $TAG_PATTERN ]]; then
    TAG_NAMES+=("$name")
    TAG_EPOCHS+=("$epoch")
  fi
done < <(git for-each-ref --sort=creatordate --format='%(refname:short)%09%(creatordate:unix)' refs/tags)

if [[ ${#TAG_NAMES[@]} -eq 0 ]]; then
  echo "ERROR: no v<semver> tags found — nothing to measure" >&2
  exit 1
fi

# Optional enrichment: gh release list for published-at timestamps. Failure
# is non-fatal — local tag creatordate already covers what we need.
GH_NOTE="local git tags"
if command -v gh >/dev/null 2>&1; then
  if gh release list --limit 1 >/dev/null 2>&1; then
    GH_NOTE="git tags + gh release list"
  else
    echo "WARN: gh unavailable/unauthenticated — using local git tags only; run \`gh auth login\` for richer data" >&2
  fi
else
  echo "WARN: gh CLI not found — using local git tags only" >&2
fi

# Indices (into TAG_NAMES/TAG_EPOCHS) of tags whose creation date falls
# within [SINCE, UNTIL].
IN_RANGE_IDX=()
i=0
while [[ $i -lt ${#TAG_NAMES[@]} ]]; do
  epoch="${TAG_EPOCHS[$i]}"
  if [[ "$epoch" -ge "$SINCE_EPOCH" && "$epoch" -le "$UNTIL_EPOCH" ]]; then
    IN_RANGE_IDX+=("$i")
  fi
  i=$((i + 1))
done

fmt_date() {
  date -d "@$1" +%Y-%m-%d 2>/dev/null || date -r "$1" +%Y-%m-%d
}

since_display=$(fmt_date "$SINCE_EPOCH")
until_display=$(fmt_date "$UNTIL_EPOCH")
range_days=$(( (UNTIL_EPOCH - SINCE_EPOCH) / 86400 ))

echo "DORA Metrics — ${since_display} to ${until_display} (${range_days} days)"
echo

# ---- Deployment Frequency --------------------------------------------------

echo "Deployment Frequency"
n=${#IN_RANGE_IDX[@]}
printf "  %-28s%s\n" "Releases in range:" "$n"

if [[ "$n" -eq 0 ]]; then
  printf "  %-28s%s\n" "Releases / week:" "n/a"
  printf "  %-28s%s\n" "Avg days between releases:" "n/a"
elif [[ "$n" -eq 1 ]]; then
  printf "  %-28s%s\n" "Releases / week:" "n/a — only one release in range"
  printf "  %-28s%s\n" "Avg days between releases:" "n/a"
else
  first_epoch="${TAG_EPOCHS[${IN_RANGE_IDX[0]}]}"
  last_epoch="${TAG_EPOCHS[${IN_RANGE_IDX[$((n-1))]}]}"
  span_days=$(( (last_epoch - first_epoch) / 86400 ))
  if [[ "$span_days" -eq 0 ]]; then
    # Multiple releases within the same calendar day — days-based rates
    # divide by zero, so report the count and skip the rate figures.
    printf "  %-28s%s\n" "Releases / week:" "n/a — releases span <1 day"
    printf "  %-28s%s\n" "Avg days between releases:" "n/a — releases span <1 day"
  else
    releases_per_week=$(awk -v n="$n" -v days="$span_days" 'BEGIN { printf "%.2f", n / (days / 7) }')
    avg_days_between=$(awk -v n="$n" -v days="$span_days" 'BEGIN { printf "%.1f", days / (n - 1) }')
    printf "  %-28s%s\n" "Releases / week:" "$releases_per_week"
    printf "  %-28s%s\n" "Avg days between releases:" "$avg_days_between"
  fi
fi
echo

# ---- Lead Time for Changes -------------------------------------------------

echo "Lead Time for Changes  (commit landed on main -> shipped in release)"

TARGET_IDX=()
if [[ "$n" -gt 0 ]]; then
  TARGET_IDX=("${IN_RANGE_IDX[@]}")
fi
if [[ "$n" -eq 0 ]]; then
  # No release in range — try the nearest tag after --until to still
  # measure commits that landed in range but haven't (yet) shipped.
  next_idx=""
  i=0
  while [[ $i -lt ${#TAG_NAMES[@]} ]]; do
    if [[ "${TAG_EPOCHS[$i]}" -gt "$UNTIL_EPOCH" ]]; then
      next_idx="$i"
      break
    fi
    i=$((i + 1))
  done
  if [[ -z "$next_idx" ]]; then
    printf "  %s\n" "range doesn't reach a shipping release yet — nothing to measure"
    echo
    echo "Data source: ${GH_NOTE}"
    exit 0
  fi
  TARGET_IDX=("$next_idx")
fi

is_target() {
  local idx="$1" t
  for t in "${TARGET_IDX[@]}"; do
    [[ "$t" == "$idx" ]] && return 0
  done
  return 1
}

LEAD_HOURS=()
commits_analyzed=0
i=0
while [[ $i -lt ${#TAG_NAMES[@]} ]]; do
  if ! is_target "$i"; then
    i=$((i + 1))
    continue
  fi

  tag_name="${TAG_NAMES[$i]}"
  tag_epoch="${TAG_EPOCHS[$i]}"
  if [[ "$i" -gt 0 ]]; then
    range_spec="${TAG_NAMES[$((i-1))]}..${tag_name}"
  else
    range_spec="$tag_name"
  fi

  while IFS=' ' read -r commit_hash author_epoch; do
    [[ -z "$commit_hash" ]] && continue
    if [[ "$author_epoch" -ge "$SINCE_EPOCH" && "$author_epoch" -le "$UNTIL_EPOCH" ]]; then
      lead_hours=$(( (tag_epoch - author_epoch) / 3600 ))
      LEAD_HOURS+=("$lead_hours")
      commits_analyzed=$((commits_analyzed + 1))
    fi
  done < <(git log --pretty=format:'%H %at' "$range_spec" 2>/dev/null || true)

  i=$((i + 1))
done

printf "  %-28s%s\n" "Commits analyzed:" "$commits_analyzed"

format_hours() {
  local hours="$1"
  if [[ "$hours" -ge 24 ]]; then
    awk -v h="$hours" 'BEGIN { printf "%.1f days", h / 24 }'
  else
    echo "${hours} hours"
  fi
}

if [[ "$commits_analyzed" -eq 0 ]]; then
  printf "  %-28s%s\n" "Median lead time:" "n/a"
  printf "  %-28s%s\n" "Mean lead time:" "n/a"
else
  sorted=()
  while IFS= read -r h; do
    sorted+=("$h")
  done < <(printf '%s\n' "${LEAD_HOURS[@]}" | sort -n)
  count=${#sorted[@]}
  mid=$((count / 2))
  if [[ $((count % 2)) -eq 1 ]]; then
    median=${sorted[$mid]}
  else
    median=$(( (sorted[$((mid-1))] + sorted[$mid]) / 2 ))
  fi
  mean=$(printf '%s\n' "${LEAD_HOURS[@]}" | awk '{ sum += $1; n++ } END { printf "%d", sum / n }')

  printf "  %-28s%s\n" "Median lead time:" "$(format_hours "$median")"
  printf "  %-28s%s\n" "Mean lead time:" "$(format_hours "$mean")"
fi

echo
echo "Data source: ${GH_NOTE}"
