#!/usr/bin/env bash
# recipe-lint.sh — record-time backstop: fail fast when a recipe needs a
# driver step type the agent's interactive driver doesn't implement (#476).
#
# The runtime arm `*) unknown step type` already aborts a recording with
# nonzero(2), but only AFTER spinning up a daemon + live CLI and burning
# tokens up to the offending step. This lint catches the same gap from
# static inspection — recipe declares step types, driver declares handled
# `case` arms — so run-cell.sh can refuse with a clear `driver_gap` before
# any of that.
#
# Sourced as a library (functions only; see recipe-lint_test.sh) AND
# runnable as a CLI: the case arms ARE the driver's capability declaration,
# so the lint reads them directly rather than trusting a hand-kept manifest
# that would drift. This file MUST NOT call `set` (it would leak options
# into a sourcing shell).

# driver_step_types_from_file <driver-file>
#   → newline-separated, sorted-unique step types the driver handles, read
#     from the `case "$type" in … esac` block. Splits grouped arms like
#     `send|slash)` into both tokens and drops the `*)` default. Empty when
#     the file has no such block (e.g. a headless-only driver).
driver_step_types_from_file() {
  local file="$1"
  [[ -f "$file" ]] || return 0
  # Drivers dispatch on $type (most) or $TYPE (opencode) — match either.
  awk '
    /case[[:space:]]+"\$[Tt][Yy][Pp][Ee]"[[:space:]]+in/ { inblk=1; next }
    inblk && /(^|[[:space:]])esac([[:space:]]|$)/ { inblk=0 }
    inblk && /^[[:space:]]*[a-z_][a-z_|]*\)/ {
      line=$0
      sub(/\).*/, "", line)            # keep the arm label, drop ) and body
      gsub(/[[:space:]]/, "", line)
      n=split(line, parts, "|")
      for (i=1; i<=n; i++) if (parts[i] != "" && parts[i] != "*") print parts[i]
    }
  ' "$file" | sort -u
}

# recipe_step_types_from_json <scenarios.json> <scenario> <agent>
#   → newline-separated, sorted-unique step types the cell's recipe needs.
#     Empty for a headless `prompt` cell (no script) — which can never hit a
#     driver-step gap.
recipe_step_types_from_json() {
  local json="$1" scenario="$2" agent="$3"
  jq -r --arg s "$scenario" --arg a "$agent" '
    .scenarios[] | select(.name == $s) | .by_adapter[$a].script // []
    | .[].type
  ' "$json" 2>/dev/null | sort -u
}

# recipe_lint_gaps <driver-file> <scenarios.json> <scenario> <agent>
#   → prints each needed step type the driver does NOT handle, one per line.
#     Returns 0 when there are no gaps, 1 when at least one gap is printed.
recipe_lint_gaps() {
  local driver="$1" json="$2" scenario="$3" agent="$4"
  local handled needed gaps
  # A missing driver file would otherwise yield an empty handled-set, making
  # every needed step look like a gap with no hint why. Say so explicitly —
  # the gap list that follows is then "all steps" because no driver exists.
  [[ -f "$driver" ]] || echo "recipe-lint: interactive driver not found: $driver" >&2
  handled="$(driver_step_types_from_file "$driver")"
  needed="$(recipe_step_types_from_json "$json" "$scenario" "$agent")"
  # Lines in `needed` absent from `handled`.
  gaps="$(comm -23 <(printf '%s\n' "$needed" | sed '/^$/d') \
                   <(printf '%s\n' "$handled" | sed '/^$/d'))"
  [[ -n "$gaps" ]] || return 0
  printf '%s\n' "$gaps"
  return 1
}

# CLI: recipe-lint.sh <scenarios.json> <scenario> <agent> [driver-file]
#   exit 0 — recipe stays inside the driver's grammar (or headless cell)
#   exit 3 — driver_gap: a step type the driver lacks (message on stderr)
# driver-file defaults to scripts/drive-<agent>-interactive.sh next to lib/.
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  set -uo pipefail
  json="${1:?usage: recipe-lint.sh <scenarios.json> <scenario> <agent> [driver-file]}"
  scenario="${2:?missing <scenario>}"
  agent="${3:?missing <agent>}"
  driver="${4:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/drive-${agent}-interactive.sh}"
  if gaps="$(recipe_lint_gaps "$driver" "$json" "$scenario" "$agent")"; then
    echo "recipe-lint ok: $agent/$scenario stays within $(basename "$driver")'s step grammar"
    exit 0
  else
    {
      echo "driver_gap: $agent/$scenario needs step type(s) $(basename "$driver") doesn't implement:"
      printf '  - gap:%s\n' $gaps
      echo "Extend the driver (a developer task) or mark the cell applicable:false — do NOT record."
    } >&2
    exit 3
  fi
fi
