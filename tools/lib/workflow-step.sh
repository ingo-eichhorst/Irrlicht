#!/usr/bin/env bash
# workflow-step.sh — read a named GitHub Actions step out of a workflow file:
# the `run: |` body it executes, and the SHELL INVOCATION the runner will
# execute that body with (#1650).
#
# ---------------------------------------------------------------------------
# Why the second half exists
#
# GitHub has TWO different bash invocations and this repo conflated them for
# the whole of the extract-and-execute family's life:
#
#   a step DECLARING `shell: bash`   →  bash --noprofile --norc -e -o pipefail {0}
#   a step declaring NOTHING         →  bash -e {0}
#
# The second is measured, not read off the docs — from run 31960152598's own
# group header for a step of replaydata-deletion-guard.yml, which declares no
# `shell:` and no `defaults:`:
#
#     guard  Detect deletions of load-bearing replaydata   shell: /usr/bin/bash -e {0}
#
# So the default is errexit ONLY: no `--noprofile`, no `--norc`, and **no
# `-o pipefail`**. Every harness in tools/lib/ that extracts a step body and
# runs it was running it under the FIRST spelling regardless, and each said in
# its own header that running the body under anything else "would grade a
# different program" — which was true, and was what they were doing.
#
# The direction of that error is what makes it worth a library rather than a
# comment fix. `pipefail` makes a pipeline report its LEFTMOST failure instead
# of only its last command's status, so a body whose correctness depends on it
#
#     manifest=$(list_things | grep -v skip)     # git fails; grep still exits 0
#
# is graded CORRECT by a harness that supplies pipefail and swallows the
# failure in production, which does not. The harness reports the step safe
# while the step is not — a false green, in the one direction that matters, of
# exactly the "absence of a finding and inability to look must never produce
# the same output" shape this whole family is about. (Under `shell: bash` the
# error runs the other way and is loud: a harness omitting pipefail would fail
# a body that CI passes.)
#
# So the invocation is derived from the workflow rather than typed: a step that
# later gains or loses a `shell:` declaration, or whose job or workflow gains a
# `defaults: { run: { shell: … } }`, moves its harness with it. Nothing here is
# a mapping anyone maintains; the workflow file is the only input.
#
# ---------------------------------------------------------------------------
# What it refuses, and why refusing is the point
#
# A derivation that cannot find its step must FAIL, never fall back to a
# default: a harness handed a plausible-looking `bash -e` for a step that no
# longer exists would go on to grade an empty body, which exits 0 and reads as
# a clean run. Every refusal below is a distinct status-2 message naming what
# could not be done:
#
#   - the workflow file is not readable
#   - no step carries that name
#   - MORE than one step carries that name (which one was being graded?)
#   - the step declares a `shell:` this library does not model
#   - (body only) the step has no `run: |` block, or an empty one
#
# Status 2 rather than 1 throughout, so a refusal is distinguishable from an
# errexit abort in a caller — the same reason shell-lib-suite.sh separates them.
#
# ---------------------------------------------------------------------------
# What the parser does and does not understand
#
# It is an indentation walk over the subset of YAML these workflows are written
# in, not a YAML parser: block sequences of steps, `run: |` block scalars, and
# `defaults: { run: { shell: } }` at workflow and job level. It deliberately
# does NOT understand flow mappings (`{shell: bash}`), other block-scalar
# spellings (`|-`, `|+`, `>`), or `shell:` values carrying a custom template
# (`bash -x {0}`) — each is a refusal rather than a guess, because a guess here
# is silently graded as fact. tools/lib/workflow-step_test.sh pins every one of
# those refusals against a committed fixture, and pins the derivation for the
# four workflows this repo actually harnesses.
#
# Key ORDER inside a step does not matter (`shell:` may follow `run:`), and
# neither does the position of a job's `defaults:` block relative to its
# `steps:` — the job's shell is resolved per job index at EOF, so a `defaults:`
# written after the steps still applies to them.

# The invocation GitHub gives each shell keyword, minus the `{0}` script
# argument the caller supplies itself. Sourced callers read these through
# workflow_step_shell; they are not a mapping to maintain per harness.
#
# `bash -e` is the DEFAULT (no `shell:`, no `defaults:`) — documented by GitHub
# as `bash -e {0}`, falling back to `sh -e {0}` where bash is absent. The
# fallback is not modelled: every runner this repo uses (ubuntu-latest,
# macos-latest) ships bash, and a harness that silently graded a body under
# `sh` would be the premise defect again in a new spelling.
WORKFLOW_STEP_SHELL_DEFAULT='bash -e'
WORKFLOW_STEP_SHELL_BASH='bash --noprofile --norc -e -o pipefail'
WORKFLOW_STEP_SHELL_SH='sh -e'

# _workflow_step_scan <workflow-file> <step-name>
#
# One pass over the file, emitting a fixed-shape record:
#
#     matches <n>          how many steps carry that name (0, 1, or more)
#     step_shell <v>       the step's own `shell:` value, or empty
#     job_shell <v>        the containing job's defaults shell, or empty
#     wf_shell <v>         the workflow's defaults shell, or empty
#     has_run <0|1>        whether the step has a `run: |` block scalar
#     body                 marker; every line after this is body
#
# Both public functions read this same record, so they cannot disagree about
# WHICH step they are describing — a harness that graded one step's body under
# another step's shell would be this issue reproduced inside its own fix.
#
# It carries no awk function, deliberately: tools/lib/shell-lib-errexit_test.sh
# walks every `tools/lib/*.sh` for shell functions and REFUSES a file
# containing the `function name {` spelling it cannot read — and it refuses on
# the text, which an awk program embedded in a shell library is also made of.
# The state machine below therefore commits the wanted step's fields as it
# learns them (`collecting`) rather than flushing a buffer from three call
# sites, which is what a helper would have been for.
_workflow_step_scan() {
  awk -v want="$2" '
    {
      col = match($0, /[^ ]/)          # 1-based column of the first non-space

      # --- inside a `run: |` block scalar: everything indented past the
      # `run:` key is body, blank lines included. Nothing in here is read as
      # structure, which is what keeps a body line like `  - foo` from being
      # mistaken for a step.
      if (in_run) {
        if (col == 0)      { if (collecting) m_body = m_body "\n"; next }
        if (col > run_col) { if (collecting) m_body = m_body substr($0, run_col + 2) "\n"; next }
        in_run = 0                 # dedent: fall through and read this line as structure
      }
      if (col == 0) next                                   # blank
      t = substr($0, col)
      if (substr(t, 1, 1) == "#") next                     # comment
      ind = col - 1

      # --- a `defaults:` block ends at the first line indented no deeper
      if (def_state && ind <= def_ind) def_state = 0

      # --- step boundaries: a sequence item inside a `steps:` list
      if (in_steps && ind <= steps_ind) { in_steps = 0; in_step = 0; collecting = 0; s_shell = "" }
      if (in_steps && t ~ /^-[ \t]+/) {
        in_step = 1; step_ind = ind; collecting = 0; s_shell = ""
        t = substr(t, match(t, /[^- \t]/))                 # read the first key inline
        ind = ind + 2                                      # ...as a step key
      } else if (in_step && ind <= step_ind) {
        in_step = 0; collecting = 0; s_shell = ""
      }

      # --- workflow / job structure
      if (ind == 0) {
        def_state = 0
        if (t ~ /^defaults:/)   { def_state = 1; def_ind = 0; def_scope = "wf"; next }
        if (t ~ /^jobs:/)       { in_jobs = 1; job_ind = -1; next }
        in_jobs = 0; next
      }
      if (in_jobs && job_ind < 0 && !in_step) { job_ind = ind }
      if (in_jobs && ind == job_ind && !in_step && t ~ /:[ \t]*$/) {
        job_idx++; in_steps = 0; next
      }

      # --- defaults: run: shell:
      if (def_state == 1 && t ~ /^run:/)   { def_state = 2; next }
      if (def_state == 2 && t ~ /^shell:/) {
        v = t; sub(/^shell:[ \t]*/, "", v); gsub(/[ \t]+$/, "", v)
        gsub(/^["'"'"']|["'"'"']$/, "", v)
        if (def_scope == "wf") wf_shell = v; else job_shell[job_idx] = v
        next
      }
      if (t ~ /^defaults:/ && !in_step) { def_state = 1; def_ind = ind; def_scope = "job"; next }

      if (t ~ /^steps:/) { in_steps = 1; steps_ind = ind; next }

      # --- step keys. The wanted step is committed as its fields are LEARNED
      # (`collecting`), so a `shell:` written before the `name:` is carried over
      # from s_shell and one written after lands straight in m_shell.
      if (in_step && ind > step_ind) {
        if (t ~ /^name:/) {
          v = t; sub(/^name:[ \t]*/, "", v); gsub(/[ \t]+$/, "", v)
          gsub(/^["'"'"']|["'"'"']$/, "", v)
          if (v == want) {
            matches++
            if (matches == 1) { collecting = 1; m_shell = s_shell; m_job = job_idx }
          }
          next
        }
        if (t ~ /^shell:/) {
          v = t; sub(/^shell:[ \t]*/, "", v); gsub(/[ \t]+$/, "", v)
          gsub(/^["'"'"']|["'"'"']$/, "", v)
          if (collecting) m_shell = v; else s_shell = v
          next
        }
        if (t ~ /^run:[ \t]*\|[ \t]*$/) {
          in_run = 1; run_col = col
          if (collecting) m_hasrun = 1
          next
        }
      }
    }
    END {
      printf "matches %d\n", matches
      printf "step_shell %s\n", m_shell
      printf "job_shell %s\n", job_shell[m_job]
      printf "wf_shell %s\n", wf_shell
      printf "has_run %d\n", m_hasrun
      printf "body\n"
      printf "%s", m_body
    }
  ' "$1"
}

# _workflow_step_field <record> <key> — the value of one header line.
_workflow_step_field() {
  printf '%s\n' "$1" | awk -v k="$2" '$1 == k { $1 = ""; sub(/^ /, ""); print; exit } $1 == "body" { exit }'
}

# _workflow_step_record <workflow-file> <step-name> — the scan, with the two
# refusals both public functions share (unreadable file, no/ambiguous step).
_workflow_step_record() {
  local wf="$1" name="$2" rec matches
  if [ ! -f "$wf" ]; then
    printf 'workflow-step: refusing — %s is not a readable file, so nothing about the "%s" step could be derived. This is NOT a default.\n' "$wf" "$name" >&2
    return 2
  fi
  rec=$(_workflow_step_scan "$wf" "$name") || {
    printf 'workflow-step: refusing — the scan of %s failed, so nothing about the "%s" step could be derived.\n' "$wf" "$name" >&2
    return 2
  }
  matches=$(_workflow_step_field "$rec" matches)
  if [ "${matches:-0}" -eq 0 ]; then
    printf 'workflow-step: refusing — %s has no step named "%s". A harness cannot grade a step that is not there, and falling back to a default would grade an empty body as a clean run.\n' "$wf" "$name" >&2
    return 2
  fi
  if [ "$matches" -gt 1 ]; then
    printf 'workflow-step: refusing — %s has %s steps named "%s", so which one a harness grades is not decidable. Rename one.\n' "$wf" "$matches" "$name" >&2
    return 2
  fi
  printf '%s\n' "$rec"
  return 0
}

# workflow_step_shell <workflow-file> <step-name>
#
# Prints the argv the runner executes the step's body with, WITHOUT the script
# path — e.g. `bash -e` or `bash --noprofile --norc -e -o pipefail`. Callers
# split it into an array and append their own script:
#
#     read -r -a argv <<<"$(workflow_step_shell "$WF" "$STEP")" || exit 1
#     "${argv[@]}" "$script"
#
# 0 on success, 2 on any refusal (message on stderr).
workflow_step_shell() {
  local wf="${1:-}" name="${2:-}" rec want src
  rec=$(_workflow_step_record "$wf" "$name") || return 2

  want=$(_workflow_step_field "$rec" step_shell); src="the step's own \`shell:\`"
  if [ -z "$want" ]; then
    want=$(_workflow_step_field "$rec" job_shell); src="the job's \`defaults: run: shell:\`"
  fi
  if [ -z "$want" ]; then
    want=$(_workflow_step_field "$rec" wf_shell); src="the workflow's \`defaults: run: shell:\`"
  fi

  case "$want" in
    '')     printf '%s\n' "$WORKFLOW_STEP_SHELL_DEFAULT"; return 0 ;;
    bash)   printf '%s\n' "$WORKFLOW_STEP_SHELL_BASH";    return 0 ;;
    sh)     printf '%s\n' "$WORKFLOW_STEP_SHELL_SH";      return 0 ;;
  esac
  printf 'workflow-step: refusing — %s: step "%s" declares `shell: %s` (via %s), which this library does not model. Add it here rather than letting a harness grade the body under the wrong shell.\n' \
    "$wf" "$name" "$want" "$src" >&2
  return 2
}

# workflow_step_body <workflow-file> <step-name>
#
# Prints the step's `run: |` body, dedented to column 0. 0 on success, 2 on any
# refusal — including a step whose body is missing or blank, because an empty
# body exits 0 under every invocation and is indistinguishable from a step that
# passes.
workflow_step_body() {
  local wf="${1:-}" name="${2:-}" rec body lines
  rec=$(_workflow_step_record "$wf" "$name") || return 2

  if [ "$(_workflow_step_field "$rec" has_run)" != "1" ]; then
    printf 'workflow-step: refusing — %s: step "%s" has no `run: |` block to extract (a `uses:` step, or a single-line `run:` this library does not model).\n' "$wf" "$name" >&2
    return 2
  fi
  body=$(printf '%s\n' "$rec" | sed '1,/^body$/d')
  lines=$(printf '%s\n' "$body" | grep -cve '^[[:space:]]*$')
  if [ "${lines:-0}" -eq 0 ]; then
    printf 'workflow-step: refusing — %s: the "%s" step body came back empty. A scan that has gone blind and a step with nothing in it must not read alike.\n' "$wf" "$name" >&2
    return 2
  fi
  printf '%s\n' "$body"
  return 0
}
