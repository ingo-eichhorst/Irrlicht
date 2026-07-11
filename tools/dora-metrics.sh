#!/usr/bin/env bash
# dora-metrics.sh — directional DORA metrics computed entirely from local
# git tags/history, plus gh release list / gh issue list as optional
# enrichment. No dashboard, no persistence — just a plain-text summary.
#
#   - Deployment Frequency, Lead Time for Changes: from git tags + commit
#     history alone.
#   - Change Failure Rate, Mean Time to Restore (#936): a release in range
#     is a "failure" if ANY of three signals fire —
#       1. hotfix: it landed within --hotfix-window-hours of the prior
#          release.
#       2. revert: it ships a commit whose subject matches ^Revert and
#          whose "This reverts commit <hash>." trailer resolves to a
#          commit shipped in an earlier release.
#       3. bug-issue: it ships a commit referencing "Fixes/Closes/Resolves
#          #NNN" for an issue labeled "bug" (requires gh).
#     MTTR is measured per flagged instance (failure shipped -> fix
#     shipped), not per unique release, so it can exceed the deduped CFR
#     count.
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
#   tools/dora-metrics.sh --hotfix-window-hours 12
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

SINCE="90 days ago"
UNTIL="now"
HOTFIX_WINDOW_HOURS=24

while [[ $# -gt 0 ]]; do
  case "$1" in
    --since) SINCE="$2"; shift 2 ;;
    --until) UNTIL="$2"; shift 2 ;;
    --hotfix-window-hours) HOTFIX_WINDOW_HOURS="$2"; shift 2 ;;
    -h|--help) sed -n '2,33p' "$0"; exit 0 ;;
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

# ISO-8601 (GitHub API timestamp shape, e.g. 2026-07-11T08:36:42Z) → epoch.
# Separate from to_epoch() above, which is tuned for user-supplied
# free-form/date-only --since/--until specs, not API timestamps.
iso_to_epoch() {
  local spec="$1"
  if date -d "$spec" +%s 2>/dev/null; then
    return 0
  fi
  date -j -f "%Y-%m-%dT%H:%M:%SZ" "$spec" +%s 2>/dev/null
}

# Optional enrichment: gh release list for published-at timestamps, and gh
# issue list for the bug-issue Change Failure Rate signal. Failure is
# non-fatal — local tag creatordate already covers Deployment
# Frequency/Lead Time, and the bug-issue signal simply drops out below.
GH_NOTE="local git tags"
BUG_ISSUE_NUMS=()
BUG_ISSUE_CREATED_EPOCH=()
if command -v gh >/dev/null 2>&1; then
  if gh release list --limit 1 >/dev/null 2>&1; then
    GH_NOTE="git tags + gh release list"
    REPO_NWO=$(gh repo view --json nameWithOwner -q .nameWithOwner 2>/dev/null || true)
    if [[ -n "$REPO_NWO" ]] && bug_json=$(gh issue list --repo "$REPO_NWO" --label bug --state all --json number,createdAt --limit 1000 2>/dev/null); then
      while IFS=$'\t' read -r num created; do
        [[ -z "$num" ]] && continue
        epoch=$(iso_to_epoch "$created") || continue
        BUG_ISSUE_NUMS+=("$num")
        BUG_ISSUE_CREATED_EPOCH+=("$epoch")
      done < <(echo "$bug_json" | jq -r '.[] | "\(.number)\t\(.createdAt)"' 2>/dev/null)
      GH_NOTE="git tags + gh release list + gh issue list (bug label)"
    else
      echo "WARN: gh issue list (bug label) failed — bug-issue CFR/MTTR signal disabled" >&2
    fi
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

format_hours() {
  local hours="$1"
  if [[ "$hours" -ge 24 ]]; then
    awk -v h="$hours" 'BEGIN { printf "%.1f days", h / 24 }'
  else
    echo "${hours} hours"
  fi
}

median_of() {
  local sorted=() h count mid
  while IFS= read -r h; do sorted+=("$h"); done < <(printf '%s\n' "$@" | sort -n)
  count=${#sorted[@]}
  mid=$((count / 2))
  if [[ $((count % 2)) -eq 1 ]]; then
    echo "${sorted[$mid]}"
  else
    echo $(( (sorted[$((mid-1))] + sorted[$mid]) / 2 ))
  fi
}

mean_of() {
  printf '%s\n' "$@" | awk '{ sum += $1; n++ } END { printf "%d", sum / n }'
}

# Linear-scan membership check against a small index set, passed as extra
# positional args (bash 3.2 has no namerefs, so this is the portable way to
# share one helper across different candidate-index arrays — e.g.
# TARGET_IDX for Lead Time, IN_RANGE_IDX for Change Failure Rate).
idx_in_set() {
  local needle="$1" t
  shift
  for t in "$@"; do
    [[ "$t" == "$needle" ]] && return 0
  done
  return 1
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

LEAD_HOURS=()
commits_analyzed=0
i=0
while [[ $i -lt ${#TAG_NAMES[@]} ]]; do
  if ! idx_in_set "$i" ${TARGET_IDX[@]+"${TARGET_IDX[@]}"}; then
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
  done < <( { git log --pretty=format:'%H %at' "$range_spec" 2>/dev/null || true; echo; } )

  i=$((i + 1))
done

printf "  %-28s%s\n" "Commits analyzed:" "$commits_analyzed"

if [[ "$commits_analyzed" -eq 0 ]]; then
  printf "  %-28s%s\n" "Median lead time:" "n/a"
  printf "  %-28s%s\n" "Mean lead time:" "n/a"
else
  printf "  %-28s%s\n" "Median lead time:" "$(format_hours "$(median_of "${LEAD_HOURS[@]}")")"
  printf "  %-28s%s\n" "Mean lead time:" "$(format_hours "$(mean_of "${LEAD_HOURS[@]}")")"
fi

echo

# ---- Change Failure Rate + Mean Time to Restore ----------------------------

tag_epoch_of() {
  local name="$1" j=0
  while [[ $j -lt ${#TAG_NAMES[@]} ]]; do
    if [[ "${TAG_NAMES[$j]}" == "$name" ]]; then
      echo "${TAG_EPOCHS[$j]}"
      return 0
    fi
    j=$((j + 1))
  done
  return 1
}

bug_issue_created_epoch() {
  local num="$1" j=0
  while [[ $j -lt ${#BUG_ISSUE_NUMS[@]} ]]; do
    if [[ "${BUG_ISSUE_NUMS[$j]}" == "$num" ]]; then
      echo "${BUG_ISSUE_CREATED_EPOCH[$j]}"
      return 0
    fi
    j=$((j + 1))
  done
  return 1
}

hotfix_window_seconds=$((HOTFIX_WINDOW_HOURS * 3600))

HOTFIX_FAILED_IDX=()
HOTFIX_MTTR_HOURS=()
REVERT_FAILED_IDX=()
REVERT_MTTR_HOURS=()
REVERT_UNRESOLVED=0
BUG_FAILED_IDX=()
BUG_MTTR_HOURS=()

i=0
while [[ $i -lt ${#TAG_NAMES[@]} ]]; do
  if ! idx_in_set "$i" ${IN_RANGE_IDX[@]+"${IN_RANGE_IDX[@]}"}; then
    i=$((i + 1))
    continue
  fi

  tag_name="${TAG_NAMES[$i]}"
  tag_epoch="${TAG_EPOCHS[$i]}"

  if [[ "$i" -gt 0 ]]; then
    prev_epoch="${TAG_EPOCHS[$((i-1))]}"
    delta=$((tag_epoch - prev_epoch))
    if [[ "$delta" -lt "$hotfix_window_seconds" ]]; then
      HOTFIX_FAILED_IDX+=("$i")
      HOTFIX_MTTR_HOURS+=("$((delta / 3600))")
    fi
    range_spec="${TAG_NAMES[$((i-1))]}..${tag_name}"
  else
    range_spec="$tag_name"
  fi

  # Walk this release's shipped commits once for both the revert and
  # bug-issue signals. %x1e/%x1f are ASCII record/unit separators — control
  # bytes that won't collide with real commit message content — so a
  # multi-line %B body can be parsed without spawning a process per commit.
  while IFS= read -r -d $'\x1e' record; do
    [[ -z "$record" ]] && continue
    commit_hash="${record%%$'\x1f'*}"
    body="${record#*$'\x1f'}"
    subject="${body%%$'\n'*}"

    if [[ "$subject" =~ ^[Rr]evert ]]; then
      if [[ "$body" =~ This\ reverts\ commit\ ([0-9a-f]+) ]]; then
        orig_hash="${BASH_REMATCH[1]}"
        orig_tag=$(git tag --contains "$orig_hash" --sort=creatordate 2>/dev/null | grep -E "$TAG_PATTERN" | head -1 || true)
        if [[ -n "$orig_tag" && "$orig_tag" != "$tag_name" ]] && orig_epoch=$(tag_epoch_of "$orig_tag"); then
          REVERT_FAILED_IDX+=("$i")
          REVERT_MTTR_HOURS+=("$(( (tag_epoch - orig_epoch) / 3600 ))")
        fi
      else
        REVERT_UNRESOLVED=$((REVERT_UNRESOLVED + 1))
      fi
    fi

    if [[ ${#BUG_ISSUE_NUMS[@]} -gt 0 ]]; then
      while IFS= read -r issue_num; do
        [[ -z "$issue_num" ]] && continue
        if created_epoch=$(bug_issue_created_epoch "$issue_num"); then
          BUG_FAILED_IDX+=("$i")
          BUG_MTTR_HOURS+=("$(( (tag_epoch - created_epoch) / 3600 ))")
        fi
      done < <(echo "$body" | grep -ioE '(fixe?s?|closes?|resolves?)[[:space:]]+#[0-9]+' | grep -oE '[0-9]+' || true)
    fi
  done < <( { git log --pretty=format:'%x1e%H%x1f%B' "$range_spec" 2>/dev/null || true; printf '\x1e'; } )

  i=$((i + 1))
done

ALL_FAILED_IDX=()
ALL_FAILED_IDX+=(${HOTFIX_FAILED_IDX[@]+"${HOTFIX_FAILED_IDX[@]}"})
ALL_FAILED_IDX+=(${REVERT_FAILED_IDX[@]+"${REVERT_FAILED_IDX[@]}"})
ALL_FAILED_IDX+=(${BUG_FAILED_IDX[@]+"${BUG_FAILED_IDX[@]}"})

UNIQUE_FAILED_IDX=()
for idx in ${ALL_FAILED_IDX[@]+"${ALL_FAILED_IDX[@]}"}; do
  dup=0
  for u in ${UNIQUE_FAILED_IDX[@]+"${UNIQUE_FAILED_IDX[@]}"}; do
    [[ "$u" == "$idx" ]] && { dup=1; break; }
  done
  [[ "$dup" -eq 0 ]] && UNIQUE_FAILED_IDX+=("$idx")
done

echo "Change Failure Rate  (deploy causing a hotfix / revert / bug-fix within range)"
printf "  %-30s%s\n" "Failures (hotfix signal):" "${#HOTFIX_FAILED_IDX[@]}"
if [[ "$REVERT_UNRESOLVED" -gt 0 ]]; then
  printf "  %-30s%s\n" "Failures (revert signal):" "${#REVERT_FAILED_IDX[@]} (${REVERT_UNRESOLVED} unresolved, non-standard revert)"
else
  printf "  %-30s%s\n" "Failures (revert signal):" "${#REVERT_FAILED_IDX[@]}"
fi
if [[ ${#BUG_ISSUE_NUMS[@]} -eq 0 ]]; then
  printf "  %-30s%s\n" "Failures (bug-issue signal):" "n/a — gh unavailable/unauthenticated"
else
  printf "  %-30s%s\n" "Failures (bug-issue signal):" "${#BUG_FAILED_IDX[@]}"
fi
if [[ "$n" -eq 0 ]]; then
  printf "  %-30s%s\n" "Failures (union, deduped):" "n/a — no release in range"
else
  cfr_pct=$(awk -v f="${#UNIQUE_FAILED_IDX[@]}" -v n="$n" 'BEGIN { printf "%.1f", (f / n) * 100 }')
  printf "  %-30s%s\n" "Failures (union, deduped):" "${#UNIQUE_FAILED_IDX[@]} / ${n}  (${cfr_pct}%)"
fi
echo

echo "Mean Time to Restore  (failure shipped -> fix shipped, per flagged instance)"

ALL_MTTR_HOURS=()
ALL_MTTR_HOURS+=(${HOTFIX_MTTR_HOURS[@]+"${HOTFIX_MTTR_HOURS[@]}"})
ALL_MTTR_HOURS+=(${REVERT_MTTR_HOURS[@]+"${REVERT_MTTR_HOURS[@]}"})
ALL_MTTR_HOURS+=(${BUG_MTTR_HOURS[@]+"${BUG_MTTR_HOURS[@]}"})

if [[ ${#HOTFIX_MTTR_HOURS[@]} -eq 0 ]]; then
  printf "  %-30s%s\n" "MTTR median (hotfix):" "n/a"
else
  printf "  %-30s%s\n" "MTTR median (hotfix):" "$(format_hours "$(median_of "${HOTFIX_MTTR_HOURS[@]}")")"
fi
if [[ ${#REVERT_MTTR_HOURS[@]} -eq 0 ]]; then
  printf "  %-30s%s\n" "MTTR median (revert):" "n/a"
else
  printf "  %-30s%s\n" "MTTR median (revert):" "$(format_hours "$(median_of "${REVERT_MTTR_HOURS[@]}")")"
fi
if [[ ${#BUG_MTTR_HOURS[@]} -eq 0 ]]; then
  printf "  %-30s%s\n" "MTTR median (bug-issue):" "n/a"
else
  printf "  %-30s%s\n" "MTTR median (bug-issue):" "$(format_hours "$(median_of "${BUG_MTTR_HOURS[@]}")")"
fi
if [[ ${#ALL_MTTR_HOURS[@]} -eq 0 ]]; then
  printf "  %-30s%s\n" "MTTR median (combined):" "n/a"
  printf "  %-30s%s\n" "MTTR mean (combined):" "n/a"
else
  printf "  %-30s%s\n" "MTTR median (combined):" "$(format_hours "$(median_of "${ALL_MTTR_HOURS[@]}")")"
  printf "  %-30s%s\n" "MTTR mean (combined):" "$(format_hours "$(mean_of "${ALL_MTTR_HOURS[@]}")")"
fi

echo
echo "Data source: ${GH_NOTE}"
