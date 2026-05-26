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

# --- Semantic lint (#496 RC3): a step type the driver ACCEPTS (a case arm) is
# not necessarily one it PRODUCES. The grammar check above can't see that; this
# layer reads scripts/lib/elicitable-primitives.json, which declares per adapter
# the step types genuinely elicited plus whether slash commands must use a
# dedicated step type (a bare send "/cmd" being a no-op on a headless driver).

# elicitable_primitives_for_agent <manifest> <agent>
#   → newline-separated, sorted-unique step types the adapter genuinely elicits.
elicitable_primitives_for_agent() {
  jq -r --arg a "$2" '.adapters[$a].elicits // [] | .[]' "$1" 2>/dev/null | sort -u
}

# agent_slash_requires_step_type <manifest> <agent> → "true" | "false"
agent_slash_requires_step_type() {
  jq -r --arg a "$2" '.adapters[$a].slash_requires_step_type // false' "$1" 2>/dev/null
}

# recipe_send_slash_texts <scenarios.json> <scenario> <agent>
#   → the text of every `send` step whose text is a bare slash command. On an
#     adapter with slash_requires_step_type, these never reach the REPL.
recipe_send_slash_texts() {
  jq -r --arg s "$2" --arg a "$3" '
    .scenarios[] | select(.name == $s) | .by_adapter[$a].script // []
    | .[] | select(.type == "send" and ((.text // "") | startswith("/"))) | .text
  ' "$1" 2>/dev/null
}

# recipe_semantic_gaps <manifest> <scenarios.json> <scenario> <agent>
#   → prints each semantic problem, one per line:
#       not-elicited:<step>   — a step type the recipe needs that the driver
#                               dispatches but does not genuinely produce.
#       slash-in-send:<text>  — a slash command embedded in send-text on an
#                               adapter that requires a dedicated slash step.
#     Returns 0 when clean, 1 when at least one problem is printed. Intended to
#     run only AFTER the grammar check passes, so `not-elicited` is purely a
#     produces-vs-accepts gap (every needed step is already a case arm).
recipe_semantic_gaps() {
  local manifest="$1" json="$2" scenario="$3" agent="$4"
  local elicits needed not_elicited slash_req slashes rc=0
  [[ -f "$manifest" ]] || return 0   # no manifest → grammar-only (back-compat)
  # No entry for this adapter → no semantic data to judge against (a brand-new
  # agent before its manifest entry is authored). Stay grammar-only.
  jq -e --arg a "$agent" '.adapters[$a]' "$manifest" >/dev/null 2>&1 || return 0
  elicits="$(elicitable_primitives_for_agent "$manifest" "$agent")"
  needed="$(recipe_step_types_from_json "$json" "$scenario" "$agent")"
  not_elicited="$(comm -23 <(printf '%s\n' "$needed"   | sed '/^$/d') \
                           <(printf '%s\n' "$elicits" | sed '/^$/d'))"
  while IFS= read -r st; do [[ -n "$st" ]] && { echo "not-elicited:$st"; rc=1; }; done <<< "$not_elicited"
  slash_req="$(agent_slash_requires_step_type "$manifest" "$agent")"
  if [[ "$slash_req" == "true" ]]; then
    slashes="$(recipe_send_slash_texts "$json" "$scenario" "$agent")"
    while IFS= read -r t; do [[ -n "$t" ]] && { echo "slash-in-send:$t"; rc=1; }; done <<< "$slashes"
  fi
  return "$rc"
}

# CLI: recipe-lint.sh <scenarios.json> <scenario> <agent> [driver-file]
#   exit 0 — recipe stays inside the driver's grammar AND every step is elicited
#   exit 3 — driver_gap: a step type the driver lacks (grammar gap)
#   exit 4 — semantic_gap: a step the driver accepts but doesn't elicit, or a
#            slash command in send-text on a slash-requires-step-type adapter
# driver-file defaults to scripts/drive-<agent>-interactive.sh next to lib/.
# manifest-file defaults to lib/elicitable-primitives.json next to this file
# (overridable as a 5th arg for testing).
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  set -uo pipefail
  json="${1:?usage: recipe-lint.sh <scenarios.json> <scenario> <agent> [driver-file]}"
  scenario="${2:?missing <scenario>}"
  agent="${3:?missing <agent>}"
  LIBDIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  driver="${4:-$(cd "$LIBDIR/.." && pwd)/drive-${agent}-interactive.sh}"
  manifest="${5:-$LIBDIR/elicitable-primitives.json}"

  # A cell with NO by_adapter.<agent> recipe entry has nothing for this lint to
  # check — but say so explicitly rather than reporting a vacuous "ok" (#496
  # RC3). A missing recipe for an applicable cell is the completeness gate's
  # job (scripts/lib/completeness-gate.sh), not a driver/recipe grammar issue.
  if [[ "$(jq -r --arg s "$scenario" --arg a "$agent" \
        '[.scenarios[] | select(.name==$s) | .by_adapter[$a]] | any(. != null)' \
        "$json" 2>/dev/null)" != "true" ]]; then
    echo "recipe-lint: no by_adapter.$agent recipe for '$scenario' — nothing to lint (completeness-gate covers missing recipes)" >&2
    exit 0
  fi

  if ! gaps="$(recipe_lint_gaps "$driver" "$json" "$scenario" "$agent")"; then
    {
      echo "driver_gap: $agent/$scenario needs step type(s) $(basename "$driver") doesn't implement:"
      printf '  - gap:%s\n' $gaps
      echo "Queue extend-driver $agent <primitive> (it ports the step type), then implement — do NOT record yet."
    } >&2
    exit 3
  fi

  if ! sem="$(recipe_semantic_gaps "$manifest" "$json" "$scenario" "$agent")"; then
    {
      echo "semantic_gap: $agent/$scenario uses step(s) the driver ACCEPTS but does not ELICIT (per $(basename "$manifest")):"
      while IFS= read -r p; do
        case "$p" in
          not-elicited:*)  echo "  - step type '${p#not-elicited:}' is dispatched but produces no effect on $agent" ;;
          slash-in-send:*) echo "  - send-text '${p#slash-in-send:}' is a slash command; use a dedicated 'slash'/'reset_session' step ($agent requires it — headless send stores it as literal text)" ;;
        esac
      done <<< "$sem"
    } >&2
    exit 4
  fi

  echo "recipe-lint ok: $agent/$scenario stays within $(basename "$driver")'s grammar and every step is elicited"
  exit 0
fi
