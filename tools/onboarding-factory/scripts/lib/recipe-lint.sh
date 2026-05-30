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
#
# #511: recipe step types come from the per-scenario shard
# (replaydata/scenarios/<coverage_id>.json → .agents[$a].details.recipe),
# read through shard-lib.sh, instead of the retired scenarios.json.

# shellcheck source=shard-lib.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/shard-lib.sh"

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

# recipe_step_types <scenario> <agent>
#   → newline-separated, sorted-unique step types the cell's recipe needs.
#     Empty for a headless `prompt` cell (no script) — which can never hit a
#     driver-step gap. Reads the shard recipe via shard-lib.
recipe_step_types() {
  shard_recipe "$1" "$2" | jq -r '.script // [] | .[].type' 2>/dev/null | sort -u
}

# recipe_lint_gaps <driver-file> <scenario> <agent>
#   → prints each needed step type the driver does NOT handle, one per line.
#     Returns 0 when there are no gaps, 1 when at least one gap is printed.
recipe_lint_gaps() {
  local driver="$1" scenario="$2" agent="$3"
  local handled needed gaps
  # A missing driver file would otherwise yield an empty handled-set, making
  # every needed step look like a gap with no hint why. Say so explicitly —
  # the gap list that follows is then "all steps" because no driver exists.
  [[ -f "$driver" ]] || echo "recipe-lint: interactive driver not found: $driver" >&2
  handled="$(driver_step_types_from_file "$driver")"
  needed="$(recipe_step_types "$scenario" "$agent")"
  # Lines in `needed` absent from `handled`.
  gaps="$(comm -23 <(printf '%s\n' "$needed" | sed '/^$/d') \
                   <(printf '%s\n' "$handled" | sed '/^$/d'))"
  [[ -n "$gaps" ]] || return 0
  printf '%s\n' "$gaps"
  return 1
}

# --- Semantic lint (#496 RC3): a step type the driver ACCEPTS (a case arm) is
# not necessarily one it PRODUCES. The grammar check above can't see that; this
# layer reads the step types the driver genuinely ELICITS plus whether slash
# commands must use a dedicated step type (a bare send "/cmd" being a no-op on a
# headless driver). Since #508 #4 these live in the driver itself as top-level
# constants (DRIVE_ELICITS / DRIVE_SLASH_REQUIRES_STEP_TYPE), so the grammar has
# ONE owner — the driver — instead of a parallel hand-kept manifest that drifts.

# driver_elicits_from_file <driver-file>
#   → newline-separated, sorted-unique step types the driver genuinely elicits,
#     read from its top-level `DRIVE_ELICITS=…` constant (space-separated).
#     Empty when the file is missing or declares no such constant.
#
#   Tolerant of the common assignment forms — double OR single quotes, and a
#   trailing `# comment` — because a strict anchored match would silently return
#   empty (→ grammar-only) on a benign edit, re-opening the semantic-lint drift
#   #508 #4 closed. Step types are `[a-z_]+`, so stripping quotes is safe.
driver_elicits_from_file() {
  local file="$1" raw
  [[ -f "$file" ]] || return 0
  raw="$(sed -n 's/^DRIVE_ELICITS=//p' "$file" | head -1)"
  [[ -n "$raw" ]] || return 0
  raw="${raw%%#*}"        # drop a trailing comment
  raw="${raw//\"/}"       # strip double quotes
  raw="${raw//\'/}"       # strip single quotes
  # Unquoted expansion word-splits on whitespace and ignores leading/trailing.
  printf '%s\n' $raw | sed '/^$/d' | sort -u
}

# driver_slash_requires_step_type <driver-file> → "true" | "false"
#   Reads the driver's top-level `DRIVE_SLASH_REQUIRES_STEP_TYPE=` constant;
#   anything other than literal `true` (incl. absent) is false. Tolerates quotes
#   and a trailing comment, like driver_elicits_from_file.
driver_slash_requires_step_type() {
  local file="$1" v
  [[ -f "$file" ]] || { echo false; return 0; }
  v="$(sed -n 's/^DRIVE_SLASH_REQUIRES_STEP_TYPE=//p' "$file" | head -1)"
  v="${v%%#*}"             # drop a trailing comment
  v="${v//[\"\' ]/}"       # strip quotes and whitespace
  [[ "$v" == "true" ]] && echo true || echo false
}

# recipe_send_slash_texts <scenario> <agent>
#   → the text of every `send` step whose text is a bare slash command. On an
#     adapter with slash_requires_step_type, these never reach the REPL.
recipe_send_slash_texts() {
  shard_recipe "$1" "$2" | jq -r '
    .script // []
    | .[] | select(.type == "send" and ((.text // "") | startswith("/"))) | .text
  ' 2>/dev/null
}

# recipe_semantic_gaps <driver-file> <scenario> <agent>
#   → prints each semantic problem, one per line:
#       not-elicited:<step>   — a step type the recipe needs that the driver
#                               dispatches but does not genuinely produce.
#       slash-in-send:<text>  — a slash command embedded in send-text on an
#                               adapter that requires a dedicated slash step.
#     Returns 0 when clean, 1 when at least one problem is printed. Intended to
#     run only AFTER the grammar check passes, so `not-elicited` is purely a
#     produces-vs-accepts gap (every needed step is already a case arm).
recipe_semantic_gaps() {
  local driver="$1" scenario="$2" agent="$3"
  local elicits needed not_elicited slash_req slashes rc=0
  [[ -f "$driver" ]] || return 0   # no driver → grammar-only (back-compat)
  elicits="$(driver_elicits_from_file "$driver")"
  # No DRIVE_ELICITS constant → no semantic data to judge against (a brand-new
  # driver before its contract is authored). Stay grammar-only.
  [[ -n "$elicits" ]] || return 0
  needed="$(recipe_step_types "$scenario" "$agent")"
  not_elicited="$(comm -23 <(printf '%s\n' "$needed"   | sed '/^$/d') \
                           <(printf '%s\n' "$elicits" | sed '/^$/d'))"
  while IFS= read -r st; do [[ -n "$st" ]] && { echo "not-elicited:$st"; rc=1; }; done <<< "$not_elicited"
  slash_req="$(driver_slash_requires_step_type "$driver")"
  if [[ "$slash_req" == "true" ]]; then
    slashes="$(recipe_send_slash_texts "$scenario" "$agent")"
    while IFS= read -r t; do [[ -n "$t" ]] && { echo "slash-in-send:$t"; rc=1; }; done <<< "$slashes"
  fi
  return "$rc"
}

# CLI: recipe-lint.sh <scenario> <agent> [driver-file]
#   exit 0 — recipe stays inside the driver's grammar AND every step is elicited
#   exit 3 — driver_gap: a step type the driver lacks (grammar gap)
#   exit 4 — semantic_gap: a step the driver accepts but doesn't elicit, or a
#            slash command in send-text on a slash-requires-step-type adapter
# driver-file defaults to scripts/drive-<agent>-interactive.sh next to lib/.
# The driver itself declares its elicited primitives (DRIVE_ELICITS) and slash
# convention (DRIVE_SLASH_REQUIRES_STEP_TYPE), so there is no separate manifest.
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  set -uo pipefail
  # Every shard read goes through jq; without it shard_has_recipe returns false
  # and the gate would fail OPEN (report "nothing to lint" for every cell). Fail
  # hard instead, like cell-integrity.sh / completeness-gate.sh.
  command -v jq >/dev/null || { echo "recipe-lint: jq is required" >&2; exit 2; }
  scenario="${1:?usage: recipe-lint.sh <scenario> <agent> [driver-file]}"
  agent="${2:?missing <agent>}"
  LIBDIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  driver="${3:-$(cd "$LIBDIR/.." && pwd)/drive-${agent}-interactive.sh}"

  # A cell with NO recipe for this agent has nothing for this lint to check —
  # but say so explicitly rather than reporting a vacuous "ok" (#496 RC3). A
  # missing recipe for an applicable cell is the completeness gate's job
  # (scripts/lib/completeness-gate.sh), not a driver/recipe grammar issue.
  if ! shard_has_recipe "$scenario" "$agent"; then
    echo "recipe-lint: no recipe for $agent/'$scenario' — nothing to lint (completeness-gate covers missing recipes)" >&2
    exit 0
  fi

  if ! gaps="$(recipe_lint_gaps "$driver" "$scenario" "$agent")"; then
    {
      echo "driver_gap: $agent/$scenario needs step type(s) $(basename "$driver") doesn't implement:"
      printf '  - gap:%s\n' $gaps
      echo "Queue extend-driver $agent <primitive> (it ports the step type), then implement — do NOT record yet."
    } >&2
    exit 3
  fi

  if ! sem="$(recipe_semantic_gaps "$driver" "$scenario" "$agent")"; then
    {
      echo "semantic_gap: $agent/$scenario uses step(s) the driver ACCEPTS but does not ELICIT (per $(basename "$driver")'s DRIVE_ELICITS):"
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
