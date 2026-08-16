#!/usr/bin/env bash
# preflight.sh — run every PR-gating CI check locally, so a failure surfaces on
# your machine in minutes instead of on GitHub Actions after a push. Exit code
# mirrors CI: 0 only if every gate that ran passed. All gates run regardless of
# an earlier failure, and a pass/fail summary prints at the end — so one
# invocation surfaces every problem instead of forcing one push per failure.
#
# Gates run CHEAPEST FIRST, in two phases, rather than in the order CI happens
# to (#1570) — there is no single CI order to mirror anyway, since test.yml,
# web-test.yml, ars-gate.yml, macos-swift.yml and linux.yml are separate
# workflows GitHub runs concurrently. See the "PHASE 1"/"PHASE 2" banners below
# for why the order is load-bearing under --budget, and why cheap-first moves
# this script towards test.yml rather than away from it.
#
# Mirrors:
#   .github/workflows/test.yml      — gofmt, core + onboarding-factory tests,
#                                      of validate, recording-rig smoke test,
#                                      shared shell libs, skill-file lint
#   .github/workflows/web-test.yml  — npm test in both web trees
#   .github/workflows/ars-gate.yml  — ARS architecture-regression gate
#                                      (composite/category score vs origin/main)
#   .github/workflows/macos-swift.yml — swift build + swift test for the
#                                      macOS app (#1509)
#   .github/workflows/linux.yml     — its replay-fixtures step natively (see
#                                      below); the rest — build + full test
#                                      suite (-race) under Linux, via the
#                                      linux-replay Docker image — is opt-in:
#                                      needs Docker, by far the slowest gate
#
# replay-fixtures is the one PR-gating step that lives in linux.yml but does
# not need Linux to be meaningful: the goldens are byte-identical across
# platforms by construction (that is what the step exists to prove), so a
# recording or parser change that breaks them breaks them here too. Running it
# natively means golden drift — the failure mode a recording sweep produces
# most often — is caught without Docker. Full Linux parity still needs
# --linux, which catches the other two things this cannot: Linux-only
# compilation and timing.
#
# The `security` group has no matching workflow — GitHub Actions doesn't run
# it yet (see .claude/skills/ir:release/SKILL.md's Step 5.5, which runs the
# same tools/security-scan.sh at release time). It's local-only for now
# because govulncheck/gosec/npm audit cost real time (~1 minute) and are
# more valuable as a pre-push gate than on every PR push; a GH Actions
# equivalent can be added later without changing tools/security-scan.sh.
#
# The `tools` group runs the unit tests for the shared shell libs under
# tools/lib/. Unlike `security` it does have a CI counterpart (test.yml's
# "Test the shared shell libs" step) — deliberately, because these are the
# tests of the pre-push gate's own scoping logic, and a gate whose only runner
# is itself is one `--no-verify` away from never running at all.
#
# The `skills` group lints .claude/skills/**/*.md — the files that tell agents
# how to triage, plan, implement and review. It exists because those files had
# zero mechanical coverage (#1209): PR #1204 changed two of them and this
# script skipped all ten gates, so thirteen green PR checks proved nothing
# about the only files that changed. It mirrors test.yml's "Lint skill files"
# step, for the same reason the `tools` group does.
#
# Usage:
#   tools/preflight.sh                 # everything except the Linux gate
#   tools/preflight.sh --linux         # + full Linux parity via Docker
#   tools/preflight.sh --only go       # just the go-test.yml-equivalent gates
#   tools/preflight.sh --only web      # just the two npm test trees
#   tools/preflight.sh --only arch     # just the ARS architecture gate
#   tools/preflight.sh --only security # just govulncheck + gosec + npm audit
#   tools/preflight.sh --only tools    # just the tools/lib shell-lib unit tests
#   tools/preflight.sh --only skills   # just the .claude/skills/**/*.md linter
#   tools/preflight.sh --only swift    # just the macOS Swift build + test suite
#   tools/preflight.sh --only posix    # just the #!/bin/sh POSIX/bashism lint
#   tools/preflight.sh --only linux    # just the Linux Docker gate
#   tools/preflight.sh --changed       # scope every gate to the packages/trees
#                                        this branch changes vs origin/main —
#                                        used by the pre-push hook so a small
#                                        push finishes in seconds. Without it
#                                        the full run above is unchanged. The
#                                        security gate is scoped twice over:
#                                        the trigger regex decides whether it
#                                        runs, then security-scan.sh --changed
#                                        picks which Go modules / web trees to
#                                        scan (issue #1213).
#   tools/preflight.sh --budget 540    # bound the WHOLE run to 540s of wall
#                                        clock. Each gate gets whatever is
#                                        left; one that outlives it is killed
#                                        and reported TIMEOUT by name, and
#                                        every gate after it is reported
#                                        NOT RUN. Both exit non-zero — neither
#                                        is a pass, and neither is a SKIP.
#                                        0 (the default) is unbounded, which is
#                                        what a manual run gets: the full run
#                                        is byte-for-byte what it was.
#
# --budget exists because the pre-push hook's failure mode was the one thing
# AGENTS.md rules out (#1570): on a one-file core/ diff the run measured 621s
# against an automated caller's 600s command budget, so the CALLER killed it
# from outside — no summary, no gate name, no exit code, the commit already
# made and the push not sent. The documented recovery, `git push --no-verify`,
# then disables every gate including the sub-second ones nobody ran. A budget
# the run enforces itself turns that silence into a named refusal.
#
# PLATFORMS overrides the Linux gate's docker --platform (default: linux/amd64,
# matching linux.yml's ubuntu-latest runner — QEMU-emulated on Apple Silicon,
# which is slow but is what CI actually runs; only override for other checks).
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$ROOT_DIR"

RUN_LINUX=0
ONLY=""
CHANGED=0
BUDGET=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --linux) RUN_LINUX=1; shift ;;
    --only)  ONLY="${2:-}"; shift 2 ;;
    --changed) CHANGED=1; shift ;;
    --budget) BUDGET="${2:-}"; shift 2 ;;
    # Print the whole leading comment block — including the Usage section,
    # which the previous fixed line range had already drifted past.
    -h|--help) awk 'NR>1 && /^#/ {print; next} NR>1 {exit}' "$0"; exit 0 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done
# An unrecognised --only value used to select no group at all: every `want`
# returned false, no gate ran, the summary printed empty and the script exited
# 0. `tools/preflight.sh --only skils` was a fully green run that checked
# nothing — the same silent-pass shape #1209 is about, reachable by one typo,
# in the harness that hosts the gate for it.
# Not `GROUPS` — that is a bash built-in holding the caller's supplementary
# group IDs, and assigning to it silently does nothing, so the check would
# compare against a list of numeric gids and reject every real group name.
VALID_GROUPS=(go web arch tools skills posix security swift linux)
if [[ -n "$ONLY" ]]; then
  known=0
  for g in "${VALID_GROUPS[@]}"; do [[ "$ONLY" == "$g" ]] && known=1; done
  if [[ "$known" != 1 ]]; then
    echo "unknown --only group: $ONLY (valid: ${VALID_GROUPS[*]})" >&2
    exit 2
  fi
fi

[[ "$ONLY" == "linux" ]] && RUN_LINUX=1

# ---- wall-clock budget (#1570) -------------------------------------------
# Sourced unconditionally, and fatally: without it every gate would run
# unbounded, which is precisely the behaviour --budget was passed to prevent.
# A bound that silently is not one is worse than no bound, because the caller
# stops watching for the outcome it was promised protection from.
# shellcheck source=lib/gate-budget.sh
. "$SCRIPT_DIR/lib/gate-budget.sh" || {
  echo "cannot load $SCRIPT_DIR/lib/gate-budget.sh — refusing to run a budgeted gate set with no budget" >&2
  exit 2
}
# ---- the shared shell-lib suite runner (#1639) ----------------------------
# Sourced unconditionally and fatally, for the same reason as the budget above:
# `shell_lib_tests` would otherwise be an unbound command, and a `tools` gate
# that cannot run must not be able to look like one that found nothing.
# shellcheck source=lib/shell-lib-suite.sh
. "$SCRIPT_DIR/lib/shell-lib-suite.sh" || {
  echo "cannot load $SCRIPT_DIR/lib/shell-lib-suite.sh — refusing to run the shell-lib gate blind" >&2
  exit 2
}

budget_open "$BUDGET" || exit 2
if budget_is_bounded; then
  echo "budget: this run is bounded to ${BUDGET}s of wall clock in total."
  echo "budget: a gate that outlives what is left is KILLED and reported TIMEOUT;"
  echo "budget: gates after it are reported NOT RUN. Neither is a pass, and both"
  echo "budget: exit non-zero. Re-run any of them unbounded with"
  echo "budget:   tools/preflight.sh --changed --only <group>"
fi

want() {
  local group="$1"
  [[ -z "$ONLY" || "$ONLY" == "$group" ]]
  return $?
}

# ---- --changed scoping ---------------------------------------------------
# In --changed mode (the pre-push hook's path) every gate is limited to the
# files this branch changes vs origin/main, so a push only re-runs the checks
# its own diff can actually break. Without --changed, CHANGED_FILES stays
# empty and changed_matches always returns true, so every gate runs
# unconditionally — the manual `tools/preflight.sh` full run is byte-for-byte
# identical to before.
#
# The changed set itself comes from tools/lib/changed-files.sh, shared with
# tools/security-scan.sh --changed. That gate decides whether to run from this
# set and then scopes its own scanners from the same one, so both layers must
# agree on what "changed" means (issue #1213).
CHANGED_FILES=""
if [[ "$CHANGED" == 1 ]]; then
  # Fatal, not a silent full-skip: without the lib the changed set would stay
  # empty, every scoped gate would record SKIP, and the run would exit 0 — a
  # green pre-push hook that ran nothing at all. Sourcing and checking in one
  # step also covers a lib that exists but is malformed, which a readability
  # test would wave through.
  # shellcheck source=lib/changed-files.sh
  . "$SCRIPT_DIR/lib/changed-files.sh" || {
    echo "cannot load $SCRIPT_DIR/lib/changed-files.sh — refusing to pass a run that would skip every gate" >&2
    exit 2
  }
  CHANGED_FILES=$(changed_files_vs_origin_main) || exit 2
fi

# changed_matches <extended-regex> — true when NOT scoping (full run) or when
# some changed file matches. Lets a full-mode gate stay unconditional.
changed_matches() {
  local re="$1"
  [[ "$CHANGED" == 1 ]] || return 0
  grep -qE "$re" <<<"$CHANGED_FILES"
}

NAMES=()
RESULTS=()
overall=0
SEPARATOR="=============================================================="

# The group whose gates are currently being run, so a TIMEOUT can name the
# exact `--only` value that re-runs it unbounded. Set by each `if want …`
# block below; the advice is useless without it.
CURRENT_GROUP=""

run_gate() {
  local name="$1"; shift
  # Checked BEFORE the banner: a gate with nothing left is one that did not
  # run, which is a different claim from one that ran and was killed, and the
  # reader has to be able to tell them apart. Both refuse the push.
  if budget_exhausted; then
    echo
    echo "$SEPARATOR"
    echo "  $name  — NOT RUN (the ${BUDGET_TOTAL_SECONDS}s budget was already spent)"
    echo "$SEPARATOR"
    # One line, not a paragraph: every gate behind the one that overran prints
    # this, and six copies of the same explanation bury the TIMEOUT above them.
    # The reasoning is stated once, in the closing block after the summary.
    echo "  Never started — that is not a SKIP and not a pass."
    echo "  Run it:  tools/preflight.sh --changed --only ${CURRENT_GROUP:-<group>}"
    NAMES+=("$name"); RESULTS+=("NOT RUN")
    overall=1
    return 0
  fi
  echo
  echo "$SEPARATOR"
  echo "  $name"
  echo "$SEPARATOR"
  local allowed rc
  allowed=$(budget_remaining)   # 0 when unbounded — budget_run reads that as "no bound"
  budget_run "$allowed" "$@"
  rc=$?
  # BUDGET_LAST_TIMED_OUT, not `rc == 124`: a gate is free to exit 124 on its
  # own, and reporting that as a timeout would send the reader hunting for
  # time nobody spent.
  if [[ "$BUDGET_LAST_TIMED_OUT" -eq 1 ]]; then
    echo
    echo "  $name — TIMEOUT."
    echo "  Killed after ${allowed}s: all that was left of this run's ${BUDGET_TOTAL_SECONDS}s budget."
    echo "  It did NOT finish, so nothing it covers is known to pass."
    echo "  Run it unbounded:  tools/preflight.sh --changed --only ${CURRENT_GROUP:-<group>}"
    NAMES+=("$name"); RESULTS+=("TIMEOUT")
    overall=1
  elif [[ "$rc" -eq 0 ]]; then
    NAMES+=("$name"); RESULTS+=("PASS")
  else
    NAMES+=("$name"); RESULTS+=("FAIL")
    overall=1
  fi
  return 0
}

# run_gate_scoped <extended-regex> <name> <cmd...> — like run_gate, but in
# --changed mode records SKIP (no effect on the exit code) unless a changed
# file matches <extended-regex>. In full mode it always runs, so behaviour is
# unchanged there.
run_gate_scoped() {
  local re="$1"; shift
  local name="$1"
  if ! changed_matches "$re"; then
    echo
    echo "$SEPARATOR"
    echo "  $name  — SKIP (no changed files match)"
    echo "$SEPARATOR"
    NAMES+=("$name"); RESULTS+=("SKIP")
    return 0
  fi
  run_gate "$@"
}

# ---- gate implementations -------------------------------------------------
# Each group's helper is defined here; the two PHASES below decide the order
# the gates run in. Phase 1 is the cheap, deterministic set — seconds at most,
# no test binaries, no network — and Phase 2 is everything that costs real
# time.
#
# That split is load-bearing, not cosmetic (#1570). Under --budget a run can
# end before it reaches its last gate, so the order decides WHICH gates survive
# a squeeze — and the cheap ones have to be the survivors. `skill-file lint`
# and `POSIX sh lint` are the only coverage their file families have anywhere,
# and each costs a fraction of a second; paying for them behind four minutes of
# `go test` is backwards. test.yml already makes this argument for this gate,
# in these words: "Runs first, before setup-go: it needs no toolchain and takes
# a fraction of a second, and behind the Go suites a `go test` failure would
# abort the job and leave a skills-only PR with no skill feedback at all." So
# ordering cheap-first moves preflight TOWARDS the workflow it mirrors.

# -- go ---------------------------------------------------------------------
gofmt_check() {
  local unformatted
  unformatted=$(gofmt -l core/ tools/)
  if [[ -n "$unformatted" ]]; then
    echo "$unformatted"
    echo "run: gofmt -w $unformatted"
    return 1
  fi
}

# changed_core_packages — import paths of the changed core/ packages that
# still exist (renames/deletions are dropped by the `go list` guard), plus the
# module-root package irrlicht/core that hosts architecture_test.go. Including
# that root package means the hexagonal-layering rules are re-checked on every
# scoped run regardless of which package changed, since architecture_test.go
# loads the whole module ("./...") itself.
changed_core_packages() {
  local pkgs=("irrlicht/core") dir
  while IFS= read -r dir; do
    [[ -z "$dir" ]] && continue
    go list "irrlicht/$dir" >/dev/null 2>&1 && pkgs+=("irrlicht/$dir")
  done < <(grep -E '^core/.*\.go$' <<<"$CHANGED_FILES" | sed -E 's#/[^/]+\.go$##' | sort -u)
  printf '%s\n' "${pkgs[@]}" | sort -u
  return 0   # callers read stdout; the pkgs list is never empty (arch root)
}

# core_module_tests — full module under -race in full mode; in --changed mode,
# only the changed packages (plus the arch-test root). A go.mod/go.sum change
# can alter any package's build, so that falls back to the full run.
core_module_tests() {
  if [[ "$CHANGED" != 1 ]] || changed_matches '^core/go\.(mod|sum)$'; then
    go test ./core/... -race -count=1
    return
  fi
  local pkgs
  pkgs=$(changed_core_packages)
  echo "scoped to changed packages:"
  printf '  %s\n' $pkgs
  go test $pkgs -race -count=1
}

# -- web (mirrors web-test.yml) ---------------------------------------------
web_tree() {
  local dir="$1"
  # --ignore-scripts mirrors web-test.yml: no dependency lifecycle script runs
  # at install time. Keep the two in step, or preflight stops being CI parity.
  ( cd "$dir" && npm ci --ignore-scripts && npm test )
  return $?
}

# -- tools (mirrors test.yml's "Test the shared shell libs" step) ------------
# Not a mirror any more — the SAME implementation (#1639). This used to be a
# second copy of the loop, and the two copies disagreed about the one thing
# that matters: this one collected every file's status, CI's aborted on the
# first failing file with the rest never run and nothing saying so. One
# implementation, for the reason macos-swift.yml gives for sharing
# swift-suite.sh: CI and the pre-push hook judge a run by the same rules rather
# than by two implementations that can disagree.
#
# The one difference that remains is deliberate and is now an ARGUMENT: CI
# passes `posix-lint_test.sh` as a skip because the macos runner ships no
# static bashism linter (linux.yml runs that file). A developer machine is
# expected to have one — the `posix` gate above refuses rather than skipping
# without it — so nothing is skipped here.
shell_lib_tests() {
  shell_lib_suite_run tools/lib
  return $?
}

# ===========================================================================
#  PHASE 1 — the cheap, deterministic gates. Sub-second to ~16s each.
# ===========================================================================

# ---- posix group (mirrors linux.yml's "Lint POSIX sh scripts" step) --------
# The #!/bin/sh corpus is three files today (site/install.sh,
# tools/linux-replay-entrypoint.sh and tools/git-hooks/shim) and the gate
# re-lints all of them whenever it fires — it is a fraction of a second, and
# the trigger cannot enumerate them anyway, because a NEW POSIX script is
# exactly the file the gate most needs to see on the push that adds it. So the
# regex is deliberately loose:
# any *.sh, any extensionless file under tools/ or site/, plus the linter and
# its own corpus. Over-firing costs milliseconds; under-firing is #1209's
# silent-skip shape again.
#
# Unlike the CI step this gate can legitimately be unrunnable on a developer
# machine that has neither checkbashisms nor shellcheck. It still FAILS rather
# than skipping in that case — the script says what to `brew install`. A gate
# whose absence looks like a pass is the defect #1423 was filed about, and
# preflight is not the place to reintroduce it.
#
# The extensionless alternatives are not decoration: `tools/git-hooks/pre-push`
# is already a tracked script with no suffix, and a `#!/bin/sh` file called
# `site/install` would match nothing in a `\.sh$`-only trigger — skipping the
# gate on precisely the push that introduces the script it exists to check.
if want posix; then
  CURRENT_GROUP=posix
  run_gate_scoped '\.sh$|^(tools|site)/[^/]*$|^tools/git-hooks/|^tools/lib/testdata/posix-lint/' \
                  "POSIX sh lint (#!/bin/sh bashisms)" tools/posix-lint.sh
fi

# ---- skills group (mirrors test.yml's "Lint skill files" step) ------------
# Scoped to skill markdown plus the linter, so editing a check re-lints the
# corpus it governs. `SKILL.md` matches at any path because skills are not all
# under .claude/skills/ — tools/irrlicht-design-system/ holds one. The whole
# corpus is linted whenever the gate fires rather than just the changed files:
# it is ~23 small files and a fraction of a second, and a finding a
# *neighbouring* file already carries is worth surfacing on the push that
# finally reads it.
if want skills; then
  CURRENT_GROUP=skills
  run_gate_scoped '^\.claude/skills/.*\.md$|(^|/)SKILL\.md$|^tools/skill-lint\.sh$' \
                  "skill-file lint" tools/skill-lint.sh
fi

# ---- tools group (mirrors test.yml's "Test the shared shell libs" step) ----
# Scoped to tools/lib/ plus every top-level tools/*.sh rather than naming the
# two current callers: a third script sourcing the lib would otherwise stop
# running these tests without anyone noticing.
if want tools; then
  CURRENT_GROUP=tools
  # go.work and .github/dependabot.yml are in scope because module-list_test.sh
  # asserts they agree — and a commit touching only one of those two is exactly
  # the commit that breaks the invariant, so leaving them out would skip the
  # test precisely when it matters (#1291).
  # site/install.sh is in the trigger set because tools/lib/install-uninstall_test.sh
  # tests it (#1416). Without it a push touching only the installer would SKIP
  # the one gate that covers the installer.
  # tools/git-hooks/ is in it for the same reason (#1591): git-hooks_test.sh
  # covers the shim and the hook scripts, and neither matches `^tools/[^/]*\.sh$`
  # — they are extensionless files one directory down.
  # .github/workflows/macos-swift.yml is in it for the same reason again
  # (#1629): swift-suite_test.sh asserts that every step there reading `$?`
  # disarms GitHub's `-e` first, and a commit editing only that workflow is
  # exactly the commit that breaks the invariant — so leaving it out would skip
  # the check precisely when it matters.
  # .github/workflows/test.yml joins them for the third time (#1639):
  # shell-lib-suite_test.sh asserts that its "Test the shared shell libs" step
  # goes through the shared runner rather than growing its own loop back, and a
  # commit editing only that workflow is again exactly the one that breaks it.
  # .github/workflows/ars.yml joins them for the fourth time (#1641):
  # ars-badge-push_test.sh EXTRACTS that workflow's "Commit badge update" step
  # and executes it, so the assertion that an exhausted push-retry fails the
  # step lives entirely in this gate — and a commit editing only ars.yml is
  # once again exactly the one that breaks it.
  run_gate_scoped '^tools/lib/|^tools/[^/]*\.sh$|^tools/git-hooks/|^go\.work$|^\.github/dependabot\.yml$|^site/install\.sh$|^\.github/workflows/(ars|macos-swift|test)\.yml$' \
                  "tools/lib shell-lib tests" shell_lib_tests
fi

# ---- go group, cheap half (mirrors test.yml's gofmt step) ------------------
# gofmt is a second of work and is the single most common way a push fails, so
# it runs before anything expensive. Unscoped, exactly as in CI: it reads the
# whole tree, not the diff.
if want go; then
  CURRENT_GROUP=go
  run_gate "gofmt" gofmt_check
fi

# ---- arch group (mirrors ars-gate.yml) -----------------------------------
# ars-gate.sh scans core/, so an ARS regression can only come from a core/
# change. ~16s, which is the boundary of "cheap" — it is the last gate in
# phase 1 for that reason.
if want arch; then
  CURRENT_GROUP=arch
  run_gate_scoped '^core/' "ARS architecture gate" tools/ars-gate.sh
fi

# ===========================================================================
#  PHASE 2 — the gates that cost real time. Measured on a one-file diff under
#  core/adapters/inbound/agents/: core module tests + replay fixtures 250s,
#  security scan 185s. These are the gates a --budget squeeze drops first, and
#  each is reported by name when it does.
# ===========================================================================

# ---- go group, expensive half (mirrors test.yml) --------------------------
if want go; then
  CURRENT_GROUP=go
  run_gate_scoped '^core/.*\.go$|^core/go\.(mod|sum)$' \
                  "core module tests"        core_module_tests
  run_gate_scoped '^tools/onboarding-factory/.*\.go$' \
                  "onboarding-factory tests" go test ./tools/onboarding-factory/... -count=1
  run_gate_scoped '^replaydata/|^tools/onboarding-factory/' \
                  "replaydata validate"      go run ./tools/onboarding-factory/cmd/of validate
  # The rig's own tests guard two file families that live OUTSIDE
  # tools/onboarding-factory/ (#1333): adapter-tables_test.sh polices
  # tools/promote-recording.sh's CLI-version table against precheck's, and
  # turn-count_test.sh covers replaydata/agents/*/turn-count.sh. Without those
  # two alternatives, `--changed` would pass a push that edits only the file a
  # new test was written to protect — the #1209 shape exactly.
  run_gate_scoped '^tools/onboarding-factory/|^tools/promote-recording\.sh$|^replaydata/agents/[^/]+/turn-count\.sh$|^replaydata/_lib/drive/' \
                  "recording-rig smoke test" bash tools/onboarding-factory/scripts/smoke-test.sh
  # linux.yml's replay-fixtures step, run natively — see the header. Scoped to
  # everything a golden is derived from: the recordings themselves, the replay
  # harness, and the core parsers/tailer the harness drives. ~3 minutes on a
  # full run, which is why the scoping matters for the pre-push hook.
  run_gate_scoped '^replaydata/|^tools/onboarding-factory/|^core/pkg/tailer/|^core/adapters/inbound/agents/' \
                  "replay fixtures"          tools/replay-fixtures.sh
  run_gate_scoped '^tools/starhistory/' \
                  "starhistory tests"        go test ./tools/starhistory/... -count=1
fi

# ---- web group (mirrors web-test.yml) -----------------------------------
if want web; then
  CURRENT_GROUP=web
  run_gate_scoped '^platforms/web/' \
                  "web: platforms/web"             web_tree platforms/web
  run_gate_scoped '^tools/onboarding-factory/internal/viewer/web/' \
                  "web: onboarding-factory viewer" web_tree tools/onboarding-factory/internal/viewer/web
fi

# ---- security group (mirrors tools/security-scan.sh's local mode; the same
# script's full mode, with GitHub Dependabot/CodeQL alert checks, runs at
# release time from ir:release's Step 5.5, not here) ------------------------
if want security; then
  CURRENT_GROUP=security
  # In --changed mode the gate's trigger regex only decides whether the scan
  # runs at all; --changed then narrows *which* modules and trees it scans, so
  # a diff that trips the trigger via one tree's lockfile doesn't also audit
  # the other tree or sweep six Go modules (issue #1213).
  security_args=(--local)
  [[ "$CHANGED" == 1 ]] && security_args+=(--changed)
  run_gate_scoped '\.go$|(^|/)go\.(mod|sum)$|(^|/)package(-lock)?\.json$' \
                  "security scan (govulncheck + gosec + npm audit)" \
                  tools/security-scan.sh "${security_args[@]}"
fi

# ---- swift group (goes BEYOND macos-swift.yml, deliberately) --------------
# Sourced unconditionally rather than inside the Darwin guard: a load failure
# must be loud wherever it happens, and `want swift` is decided later.
. "$SCRIPT_DIR/lib/swift-suite.sh" || {
  echo "cannot load $SCRIPT_DIR/lib/swift-suite.sh — refusing to run the Swift gate blind" >&2
  exit 1
}
# The macOS app had no automated floor of any kind until #1509: no CI workflow
# built or tested Swift, and preflight had no Swift gate either, so a
# platforms/macos-only diff ran *every* gate as SKIP and pushed green having
# checked nothing.
#
# Since #1530 macos-swift.yml runs the same suite through the same harness, so
# this gate is no longer the ONLY place the macOS tests run. It is still the
# stronger of the two and the difference is worth naming: a runner has a
# virtual display, a stock font set and no usable audio stack, so a green there
# is a green against a machine the app never ships to. The parity rule
# elsewhere in this file is "mirror CI exactly"; here CI is a floor and this is
# the real gate.
#
# What #1530 removed was the four host dependencies that made a runner
# structurally unable to run it: image snapshots rasterising at the main
# SCREEN's backing scale, a modal Sparkle alert that hung whichever test next
# spun the run loop, #1523's helper deadlock, and a path scorer keyed on the
# process's own $HOME.
#
# `--skip LauncherHarnessTests` matches the workflow exactly and is
# load-bearing beyond speed: that target drives real terminal applications
# through NSRunningApplication, so an unfiltered run on a developer machine
# reaches out and manipulates live windows. It is separately gated on
# TEST_HARNESS=1 in the source; the skip is what holds if that is relaxed.
#
# Scoped to platforms/macos plus the workflow, since nothing else can break it
# — the Swift app talks to the daemon over HTTP, not by importing Go.
swift_suite() {
  if ! command -v swift >/dev/null 2>&1; then
    # Loud, not a silent pass: a gate whose absence reads as success is the
    # failure mode #1423 and #1209 were both about. Reachable only ON macOS —
    # see the platform guard below for why a Linux host is a different case.
    echo "swift not found — install Xcode or the Swift toolchain" >&2
    return 1
  fi

  ( cd platforms/macos && swift build ) || return 1

  # The suite runs through tools/lib/swift-suite.sh rather than being invoked
  # directly, because this gate's exit code was not a sufficient signal and the
  # gap was not visible from here. XCTest answers a hung expectation by
  # `abort()`ing the process: the run stops partway (33 of 40 suites, measured),
  # the aggregate total never prints, and every suite that already reported says
  # "0 failures" — because none of them failed, the rest simply never ran. The
  # helper additionally bounds the run, since the other shape of the same fault
  # is a process that never returns at all, which left this gate — and therefore
  # the pre-push hook — hanging indefinitely. See #1523.
  #
  # Both names below: `LauncherTestHarness` is the target, `LauncherHarnessTests`
  # the class. Either alone excludes the harness today; the pair is what keeps
  # that true after a class is added to the target or the target is renamed.
  # This matters more locally than in CI — here an unskipped harness test drives
  # the developer's own live terminal windows.
  local log rc
  log=$(mktemp -t irrlicht-swift-suite) || return 1
  ( cd platforms/macos && swift_suite_run "$log" \
      swift test --skip LauncherTestHarness --skip LauncherHarnessTests )
  rc=$?
  # No `cat "$log"` here: swift_suite_run streams the run live as well as
  # capturing it, so re-printing would double every line.
  swift_suite_verdict "$rc" "$log"
  rc=$?
  rm -f "$log"
  return "$rc"
}

if want swift; then
  CURRENT_GROUP=swift
  # macOS-only, and skipped rather than failed elsewhere. The gate mirrors a
  # workflow that is itself `runs-on: macos-latest`, so on Linux it is out of
  # scope, not unmet — without this guard a Linux contributor's plain
  # `tools/preflight.sh` could never go green, since the gate is in the default
  # set and `swift_suite` fails hard on a missing toolchain. The distinction
  # this preserves is the one that matters: "this platform does not run this
  # check" is a SKIP, while "this platform runs it and the tool is missing" is
  # a FAIL.
  if [[ "$(uname -s)" == "Darwin" ]]; then
    # tools/lib/swift-suite.sh is in the trigger set because the harness this
    # gate runs lives there rather than in this file — without it, a push that
    # rewrites the watchdog skips the one gate that exercises it against a real
    # `swift test`. Same reasoning as site/install.sh's entry in the tools gate.
    run_gate_scoped '^platforms/macos/|^\.github/workflows/macos-swift\.yml$|^tools/preflight\.sh$|^tools/lib/swift-suite(_test)?\.sh$' \
                    "macOS Swift build + test" swift_suite
  else
    echo
    echo "$SEPARATOR"
    echo "  macOS Swift build + test  — SKIP (not macOS)"
    echo "$SEPARATOR"
    NAMES+=("macOS Swift build + test"); RESULTS+=("SKIP")
  fi
fi

# ---- linux group (mirrors linux.yml, opt-in: --linux or --only linux) ---
linux_parity() {
  command -v docker >/dev/null 2>&1 || { echo "docker not found — install Docker or skip this gate"; return 1; }
  local plat tag
  plat="${PLATFORMS:-linux/amd64}"
  tag="irrlicht-linux-preflight:${plat//[,\/]/-}"
  docker buildx build --platform "$plat" --load -f tools/linux-replay.Dockerfile -t "$tag" . || return 1
  docker run --rm --platform "$plat" "$tag" \
    bash -c "cd /src/core && go build ./... && go test ./... -race -count=1 && cd /src && tools/replay-fixtures.sh"
}

if [[ "$RUN_LINUX" == 1 ]]; then
  CURRENT_GROUP=linux
  run_gate "linux parity (build + go test ./... -race + replay-fixtures)" linux_parity
fi

echo
echo "$SEPARATOR"
echo "  summary"
echo "$SEPARATOR"
if [[ ${#NAMES[@]} -eq 0 ]]; then
  # Belt to the --only braces above: a run that recorded no gate at all is not
  # a pass, whatever selected it down to nothing.
  echo "  (no gates ran — refusing to report a green run that checked nothing)"
  exit 2
fi
for i in "${!NAMES[@]}"; do
  printf "  %-58s %s\n" "${NAMES[$i]}" "${RESULTS[$i]}"
done

# Name the unfinished gates once more, after the table (#1570). The table is
# the first thing scrolled past; the sentence that decides whether to push is
# the last thing printed. SKIP is deliberately absent from this list — "this
# diff cannot break it" is a finished answer, where TIMEOUT and NOT RUN are the
# absence of one.
unfinished=()
for i in "${!NAMES[@]}"; do
  case "${RESULTS[$i]}" in
    TIMEOUT|"NOT RUN") unfinished+=("${NAMES[$i]} (${RESULTS[$i]})") ;;
  esac
done
if [[ ${#unfinished[@]} -gt 0 ]]; then
  echo
  echo "  ${#unfinished[@]} gate(s) did not finish inside the ${BUDGET_TOTAL_SECONDS}s budget:"
  printf '    - %s\n' "${unfinished[@]}"
  echo "  They are NOT passes. Run them unbounded before pushing, or push with a"
  echo "  larger budget:  PREPUSH_BUDGET=1800 git push"
fi

exit "$overall"
