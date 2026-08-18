#!/usr/bin/env bash
# bash-lint.sh — run a static analyser over every BASH script this repo ships.
#
# WHY THIS EXISTS (#1684). `tools/posix-lint.sh` selects files whose FIRST LINE
# is a `#!/bin/sh` shebang, deliberately and for a good reason (#1611 — a
# content grep would try to lint a bash file that writes `#!/bin/sh` stubs
# inside a heredoc). The consequence was that every `bash` file in the repo sat
# outside every static linter here: measured at the time this was written,
# 23 files / 8,431 lines under `tools/lib/` alone, including the bounded gate
# runner (`gate-budget.sh`), the pre-push hook's scoping rules
# (`changed-files.sh`), the shared suite runner (`shell-lib-suite.sh`), the
# workflow-step resolver and the Swift harness. That is the machinery that
# decides whether every OTHER gate passes, and nothing read it.
#
# It is the same file-selection blindness class as #1423 and #1611: a gate
# whose selection rule silently excludes a whole family reads exactly like a
# gate that passed. So this one is DEFAULT-IN — every bash file git knows
# about is linted unless a declared exclusion says otherwise, with a reason,
# and an exclusion that stopped matching anything is a hard refusal rather
# than a silent no-op (see EXCLUDE below). A bash file added anywhere in the
# repo is covered by existing rather than by someone remembering to widen a
# prefix list.
#
# WHAT IT FOUND when it was first run over the corpus it now guards — 26
# findings over 81 files / 18,304 lines, all fixed or annotated in the PR that
# added this file:
#
#     SC2034 ×17 variable assigned and never used *in this file*. Every one a
#                genuine cross-file seam (a knob a sourced library reads, an
#                output variable a caller reads, an env var the daemon reads),
#                so every one is a per-SITE disable naming its consumer's
#                file:line — never a code deleted from the gate.
#     SC2115 ×2  `rm -rf "$NOLIB/lib"` → `rm -rf /lib` when the var is empty.
#                `set -u` does NOT catch an empty variable, and `$(mktemp -d)`
#                yields empty on failure. This shape is why the issue was filed.
#     SC2209 ×2  `kind=file` — a FALSE POSITIVE, and checked rather than
#                assumed: the assertion two lines down compares against the
#                literal `"file"`, so the string is what was meant; shellcheck
#                reads it as a missing `$(…)` because `file(1)` is a command.
#     SC2164 ×2  `cd "$DIR"` with no `|| exit`, in preflight.sh and
#                security-scan.sh — both of which then run every gate against
#                whatever tree the caller happened to be in.
#     SC1087 ×1  `$agent[` read as an array expansion.
#     SC2155 ×1  declare-and-assign masking a return value.
#     SC2188 ×1  `> "$LOG"` — a redirection with no command.
#
# WHAT THE SECOND WAVE FOUND (#1687), when the two remaining exclusions — the
# recording rig's per-agent drivers under `replaydata/` and the `.tmpl` they are
# generated from — came in. 89 findings over 34 files (79 under `replaydata/`,
# 10 in the template), and the census is IDENTICAL on 0.9.0 and 0.11.0, which
# extends the version-agreement measurement above to this corpus rather than
# assuming it:
#
#     SC2034 ×78 the driver protocol's declared-variable seam, and every one
#                verified to have a real consumer before it was annotated:
#                `DRIVE_ELICITS` / `DRIVE_SLASH_REQUIRES_STEP_TYPE` (24) are
#                scraped out of the driver's SOURCE TEXT by
#                scripts/lib/recipe-lint.sh and never expanded in a shell at
#                all; the `SES_*` slot arrays and the view vars (48) are
#                read/written by the sourced `replaydata/_lib/drive/*.sh`;
#                `SETTINGS_PATH`/`UUID` (5) name positional slots of the
#                driver protocol run-cell.sh:379 invokes. One — `SKILL_DIR` in
#                the gastown driver — had NO consumer anywhere in the repo and
#                was deleted rather than annotated.
#     SC1125 ×8  `# shellcheck disable=SC2086 — <prose>`. The disable IS
#                honoured (re-measured on both versions), so the fix is a
#                respelling, not a behaviour change.
#     SC1072/3×2 `replaydata/_lib/drive/contracts.sh`, whose whole analysis was
#                abandoned — see the note on comments below.
#     SC2155 ×1  `local session="ocdrv-$$-$(date +%s)"` in opencode's driver.
#
# Why the drivers were worth bringing in, beyond the count: #1388/#1694 found
# that codex's driver had ROTTED against codex-cli 0.147.0 and produced a
# healthy-looking fixture — `driver.exit-reason: ok`, zero rollouts — from a run
# that did nothing. Ten adapter drivers steer live TUIs by grepping literal
# strings, and until #1687 no static analyser read one of them.
#
# ONE FILE AT A TIME, and that is not an implementation detail. Measured: given
# several files in ONE invocation, shellcheck suppresses SC2034 for a name used
# in ANY of them — `shellcheck tools/lib/swift-suite_test.sh` reports 2
# findings, `shellcheck tools/lib/swift-suite_test.sh tools/lib/await-gone.sh`
# reports 0. A multi-file gate's verdict would therefore depend on which OTHER
# files happened to be in the same command, which under `--changed` scoping
# differs between a developer's push and CI's full run. Per-file is the only
# deterministic choice, and it is why the count above is 26 where a single
# multi-file invocation over the same tree reports 15.
#
# ─── THE SEVERITY FLOOR IS `warning`, AND THAT IS A MEASUREMENT ─────────────
#
# `tools/posix-lint.sh` filters shellcheck's output to a NAMED CODE SET rather
# than a severity band, so general style debt cannot be dragged into a gate
# about POSIX compatibility. That precedent was weighed and deliberately not
# followed here, because the two gates want different things: SC3xxx is a
# closed family by definition ("In POSIX sh, X is undefined"), whereas "bugs in
# bash" has no such family — an opt-in list of codes means a code shellcheck
# adds later is not enforced until someone remembers to add it, which is the
# "covered by remembering" shape AGENTS.md argues against throughout. A
# severity FLOOR is covered by existing: `error` and `warning` codes added by a
# future shellcheck are enforced the day the runner's image bumps.
#
# What the floor has to survive is a VERSION SPLIT: CI's ubuntu image ships
# 0.9.0, a developer's brew ships 0.11.0. Measured per-file over the whole
# in-scope corpus (81 files / 18,304 lines at the commit this landed on), with
# 0.9.0 and 0.10.0 binaries fetched from koalaman's releases and run beside the
# local 0.11.0:
#
#     -S warning   0.9.0 → 26   0.10.0 → 26   0.11.0 → 26   BYTE-IDENTICAL
#     -S style     0.9.0 → 312  0.10.0 → 312  0.11.0 → 199  163 disagreements
#
# At `warning` the version asymmetry runs in NEITHER direction: same codes, same
# files, same line:col, across two minor versions. At `style` it runs in BOTH,
# and dominantly in the direction that hurts — 138 findings CI's 0.9.0 reports
# which a local 0.11.0 cannot even produce (137 × SC2317 "command appears
# unreachable", plus one SC2015), against 25 the other way (SC2329 "function
# never invoked", which 0.9.0 does not have). A developer's
# `tools/preflight.sh --only bash` would be green while linux.yml went red on
# 138 findings their own shellcheck does not implement — the round-trip that
# posix-lint.sh's monotonicity argument exists to prevent, running in the worse
# of the two directions. So `style` is out of scope on evidence rather than on a
# guess about noise, and the floor is the band where three versions provably
# agree.
#
# `--external-sources` (`-x`) is OFF, also measured: these libraries are
# sourced through variable paths (`. "$DIR/await-gone.sh"`), which shellcheck
# cannot resolve without a `source-path`, so `-x` changed the finding count by
# exactly zero. It is not on because it would buy nothing while making the
# verdict depend on the caller's cwd.
#
# The escape hatch is a per-SITE `# shellcheck disable=SCxxxx` carrying a
# reason, which is a reviewable diff at the place the exemption applies. There
# is deliberately no repo-wide code-exclusion list — the same choice
# `core/architecture_test.go` and `core/architecture_hookbody_test.go` make.
# Note the spelling: the reason goes behind a SECOND `#`. Measured on both
# 0.9.0 and 0.11.0, `# shellcheck disable=SC2034 — reason` still applies the
# disable but reports SC1125 at ERROR severity for the trailing prose, so this
# gate rejects it — which is how eight such directives in replaydata/ were
# found.
#
# WHERE IT RUNS. `.github/workflows/linux.yml`'s `build-test` job, beside
# `posix-lint.sh`, because that image already ships shellcheck. The reason is
# NOT the one posix-lint has, and the difference matters: posix-lint needs
# ubuntu because `/bin/sh` must genuinely BE dash there, i.e. the property
# under test is a property of the runtime. shellcheck is a static analyser over
# the bash language and reads no interpreter at all — the measurements above
# were taken with a 0.9.0 x86_64 binary under Rosetta on arm64 macOS and agree
# byte-for-byte with a native arm64 0.11.0, so neither the OS nor the arch
# enters the verdict. What picks the host is purely that macos-latest ships no
# static linter at all, so hosting this in test.yml would have to
# `brew install` one on every PR. Mirrored locally by
# `tools/preflight.sh --only bash`.
#
# A NOTE ON THIS FILE'S OWN COMMENTS, which is not trivia. A comment line whose
# FIRST word after `#` is the linter's name is parsed as a DIRECTIVE, and an
# unparseable one (SC1072/SC1073) makes it ABANDON the file — every later
# finding silently disappears. This header tripped exactly that on its first
# run, and `replaydata/_lib/drive/contracts.sh` carried it from #508 until
# #1687. So never open a comment line with that word — and note that the gate
# below catches it, because an abandoned analysis reports SC1072/SC1073 at
# ERROR severity and therefore fails rather than reading as clean.
#
# The floor was checked against that file rather than assumed, and the check
# was worth running because it corrects the issue's own framing. #1687 quoted
# "rewording that one line surfaces an SC2005 the file currently hides", which
# is true at shellcheck's DEFAULT severity and false at this gate's: SC2005 is
# `style`, so at `--severity=warning` the reworded file reports nothing at all.
# What the floor catches is therefore not the hidden finding but the
# ABANDONMENT itself — the file goes from "2 errors and no analysis" to
# "analysed, clean", and any finding a future edit introduces is now visible
# instead of swallowed. That is the whole reason SC1072/SC1073 sit inside the
# floor, and the reason no extra guard was added for the construct: the gate
# already refuses it, measured on 0.9.0 and 0.11.0 alike.
#
# NEVER SILENTLY PASSES. Four ways out are hard refusals (exit 2), not skips:
# no shellcheck, an empty in-scope file set, an exclusion rule that matches
# nothing, and a file git listed that cannot be opened. And a shellcheck that
# exits >1, or exits 1 while printing nothing, is reported as a failure to
# CHECK rather than as a clean file — that is #1423's founding incident, where
# a linter that failed to run printed `ALL PASS` over an installer carrying a
# deliberate `[[ ]]`.
#
# Usage:
#   tools/bash-lint.sh              # lint every discovered bash script
#   tools/bash-lint.sh FILE...      # lint just these files (used by tests;
#                                     exclusions and discovery do not apply)
#   tools/bash-lint.sh --list       # print the in-scope file list, one per line
set -uo pipefail

FILES=()
LIST_ONLY=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    # Print the whole leading comment block, the same self-documenting idiom
    # tools/posix-lint.sh and tools/preflight.sh use, so --help can't drift.
    -h|--help) awk 'NR>1 && /^#/ {print; next} NR>1 {exit}' "$0"; exit 0 ;;
    --list) LIST_ONLY=1; shift ;;
    --) shift; FILES+=("$@"); break ;;
    -*) echo "unknown arg: $1" >&2; exit 2 ;;
    *) FILES+=("$1"); shift ;;
  esac
done

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"

# ─── Declared exclusions ────────────────────────────────────────────────────
# Pairs of <glob> <reason>. Everything else that carries a bash shebang IS
# linted, so the gate cannot go blind to a whole directory by omission — only
# by an entry here, which is a reviewable diff carrying its own justification.
#
# Each glob is matched against the repo-root-relative path with `[[ == ]]`, and
# each MUST match at least one discovered file or the gate refuses (below). An
# exclusion that stopped matching is indistinguishable from coverage from the
# log, which is the same silent-green shape this gate is about.
EXCLUDE=(
  '*/testdata/*'
  'a deliberately-corrupt fixture corpus, driven by its own unit test rather than by this gate — the split tools/skill-lint.sh and tools/posix-lint.sh already draw for theirs'
)

# ─── Discovery ──────────────────────────────────────────────────────────────

# is_bash_shebang <file> — true when line 1 selects a bash interpreter.
# Reads ONE line rather than slurping: only line 1 can carry a shebang, and
# matching on line 1 alone is what stops a script that quotes another
# interpreter's shebang inside a heredoc from being misclassified (#1611's
# lesson, applied in the mirror direction).
is_bash_shebang() {
  local first
  IFS= read -r first < "$1" 2>/dev/null || return 1
  case "$first" in
    '#!'*bash) return 0 ;;
    '#!'*bash\ *) return 0 ;;
    *) return 1 ;;
  esac
}

EXCLUDED_COUNT=()   # parallel to EXCLUDE's glob entries: how many each matched
DISCOVERED=0

if [[ ${#FILES[@]} -eq 0 ]]; then
  cd "$ROOT_DIR" || exit 2

  # The walk is `tools/posix-lint.sh`'s, deliberately in lockstep with it
  # rather than re-derived — that file's header carries the full reasoning and
  # the incidents behind each flag. In brief: `git ls-files` and not `find`,
  # because `find .` descends into `.claude/worktrees/` and would lint every
  # other branch's scripts as if they were this one's; `-z` with
  # `core.quotePath=off`, because the default C-quotes any non-ASCII path into
  # a string that is not a real path and the file then drops out of the walk
  # with no message; and the second `ls-files --others --exclude-standard`,
  # because a brand-new untracked script is exactly the file that puts this
  # gate in scope via `tools/preflight.sh --changed` (#1611).
  for ((ei = 0; ei < ${#EXCLUDE[@]}; ei += 2)); do
    EXCLUDED_COUNT+=(0)
  done

  while IFS= read -r -d '' f; do
    is_bash_shebang "$f" || continue
    if [[ ! -f "$f" ]]; then
      # Loud, not silent. A path git listed but the walk cannot open is the
      # blind spot this gate exists to remove, and "not looked at" must never
      # render as "clean".
      echo "bash-lint: refusing — $f was listed by git but is not a regular file" >&2
      exit 2
    fi
    DISCOVERED=$((DISCOVERED + 1))
    excluded=0
    slot=0
    for ((ei = 0; ei < ${#EXCLUDE[@]}; ei += 2)); do
      # shellcheck disable=SC2053  # the RHS is a GLOB on purpose — that is what
      # an exclusion rule is. Quoting it would turn every entry into a literal
      # path comparison and silently exclude nothing, which the refusal below
      # would then report as a broken rule rather than as coverage.
      if [[ "$f" == ${EXCLUDE[ei]} ]]; then
        EXCLUDED_COUNT[slot]=$(( ${EXCLUDED_COUNT[slot]} + 1 ))
        excluded=1
        break
      fi
      slot=$((slot + 1))
    done
    [[ "$excluded" -eq 1 ]] && continue
    FILES+=("$f")
  done < <({
    git -c core.quotePath=off ls-files -z
    git -c core.quotePath=off ls-files --others --exclude-standard --full-name -z -- :/
  } | LC_ALL=C sort -z -u)

  # An exclusion that matches nothing is a REFUSAL, not a no-op. The repo's own
  # idiom for exemption maps — `TW_EXEMPT_KEYS` in shell-lib-errexit_test.sh,
  # `nilTolerant` in core/application/services/construction_test.go — is that
  # keys are existence-checked, because an entry that stopped naming anything
  # real reads from the log exactly like coverage.
  slot=0
  for ((ei = 0; ei < ${#EXCLUDE[@]}; ei += 2)); do
    if [[ "${EXCLUDED_COUNT[slot]}" -eq 0 ]]; then
      echo "bash-lint: refusing — the exclusion '${EXCLUDE[ei]}' matched no discovered bash file." >&2
      echo "  Its stated reason was: ${EXCLUDE[ei + 1]}" >&2
      echo "  Either the family moved or it is gone; an exclusion that stopped excluding must not read as coverage." >&2
      exit 2
    fi
    slot=$((slot + 1))
  done
fi

if [[ ${#FILES[@]} -eq 0 ]]; then
  # Not a pass. An empty set means discovery and the repo disagree about where
  # bash lives — the same silent-green shape as above, one layer up.
  echo "bash-lint: refusing — no bash scripts found, so nothing was checked." >&2
  exit 2
fi

if [[ "$LIST_ONLY" -eq 1 ]]; then
  printf '%s\n' "${FILES[@]}"
  exit 0
fi

# ─── Tool discovery ─────────────────────────────────────────────────────────
# One linter, and its absence is a hard failure with an install hint rather
# than a skip. "The linter wasn't there" must not read as "the code is clean" —
# that is the exact failure mode #1423 is about, and it is the one thing that
# would make this gate the defect it was built to remove.
#
# Only shellcheck: `checkbashisms`, posix-lint.sh's other linter, exists to
# find bash-isms in POSIX sh and has nothing to say about a bash file.
if ! command -v shellcheck >/dev/null 2>&1; then
  echo "bash-lint: no shellcheck found — refusing to pass a run that checked nothing." >&2
  echo "  Install it:  brew install shellcheck   |   apt-get install -y shellcheck" >&2
  echo "  CI's ubuntu image ships 0.9.0; this gate is the check that guards main." >&2
  exit 2
fi

SC_VERSION="$(shellcheck --version 2>/dev/null | awk '/^version:/ {print $2}')"
[[ -z "$SC_VERSION" ]] && SC_VERSION="unknown"
SEVERITY=warning

# lint_one <file> — print findings to stdout, return 1 when any were found OR
# when shellcheck could not be trusted to have looked.
#
# EVERY BRANCH DISTINGUISHES "clean" FROM "did not run", which is why the exit
# status is read explicitly instead of testing a captured string for emptiness.
# The obvious spelling — `out=$(shellcheck … | grep …); [[ -z "$out" ]]` —
# reproduces #1423 inside the gate: a linter or filter that fails to execute
# leaves the capture empty, empty reads as clean, and the file is reported ok.
# posix-lint.sh's first draft did exactly that and printed `ALL PASS` over an
# installer carrying a deliberate `[[ ]]`.
#
# `--shell=bash` is explicit rather than inferred from the shebang so the
# gate's verdict cannot depend on how a shebang is spelled; discovery has
# already established that every file here is bash.
lint_one() {
  local f="$1" raw rc
  raw=$(shellcheck --shell=bash --severity="$SEVERITY" --format=gcc "$f" 2>&1); rc=$?
  case "$rc" in
    0) return 0 ;;
    1)
      # rc 1 means "findings", so an EMPTY capture here means the output was
      # lost, not that the file is clean. Absence of a finding and inability to
      # look must not produce the same answer.
      if [[ -z "$raw" ]]; then
        printf 'shellcheck exited 1 (findings) but printed nothing — the output was lost, so this file was NOT checked.\n'
        return 1
      fi
      printf '%s\n' "$raw"
      return 1 ;;
    *)
      printf 'shellcheck could not check this file (exit %s):\n%s\n' "$rc" "$raw"
      return 1 ;;
  esac
}

# ─── Run ────────────────────────────────────────────────────────────────────

# Name what ran, over how much, under which rules. A run that cannot be read
# back to "what actually looked at this" is a run whose green means nothing in
# particular — and the excluded census is half of that: an exclusion widening
# to swallow the corpus would otherwise look like a clean gate.
if [[ "$DISCOVERED" -gt 0 ]]; then
  excluded_note=""
  slot=0
  for ((ei = 0; ei < ${#EXCLUDE[@]}; ei += 2)); do
    excluded_note+=" ${EXCLUDE[ei]}=${EXCLUDED_COUNT[slot]}"
    slot=$((slot + 1))
  done
  echo "bash-lint: ${#FILES[@]} of $DISCOVERED bash file(s); excluded:$excluded_note"
else
  echo "bash-lint: ${#FILES[@]} file(s) named on the command line"
fi
echo "bash-lint: shellcheck $SC_VERSION, --severity=$SEVERITY, external-sources=off"

rc=0
for f in "${FILES[@]}"; do
  if out=$(lint_one "$f"); then
    echo "  ok  $f"
  else
    echo "FAIL $f" >&2
    printf '%s\n' "$out" >&2
    rc=1
  fi
done

if [[ "$rc" -ne 0 ]]; then
  echo >&2
  echo "bash-lint: FAILED — the above are shellcheck findings at severity $SEVERITY or above." >&2
  echo "  Fix them, or add a per-site '# shellcheck disable=SCxxxx  # <reason>' where the" >&2
  echo "  finding is a false positive. Do not disable a code repo-wide." >&2
  exit 1
fi

echo "bash-lint: ALL PASS"
