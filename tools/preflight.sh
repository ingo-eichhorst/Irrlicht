#!/usr/bin/env bash
# preflight.sh — run every PR-gating CI check locally, in the same order CI
# runs them, so a failure surfaces on your machine in minutes instead of on
# GitHub Actions after a push. Exit code mirrors CI: 0 only if every gate
# that ran passed. All gates run regardless of an earlier failure, and a
# pass/fail summary prints at the end — so one invocation surfaces every
# problem instead of forcing one push per failure.
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
while [[ $# -gt 0 ]]; do
  case "$1" in
    --linux) RUN_LINUX=1; shift ;;
    --only)  ONLY="${2:-}"; shift 2 ;;
    --changed) CHANGED=1; shift ;;
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

run_gate() {
  local name="$1"; shift
  echo
  echo "$SEPARATOR"
  echo "  $name"
  echo "$SEPARATOR"
  if "$@"; then
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

# ---- go group (mirrors test.yml) ----------------------------------------
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

if want go; then
  run_gate        "gofmt"                    gofmt_check
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
web_tree() {
  local dir="$1"
  # --ignore-scripts mirrors web-test.yml: no dependency lifecycle script runs
  # at install time. Keep the two in step, or preflight stops being CI parity.
  ( cd "$dir" && npm ci --ignore-scripts && npm test )
  return $?
}

if want web; then
  run_gate_scoped '^platforms/web/' \
                  "web: platforms/web"             web_tree platforms/web
  run_gate_scoped '^tools/onboarding-factory/internal/viewer/web/' \
                  "web: onboarding-factory viewer" web_tree tools/onboarding-factory/internal/viewer/web
fi

# ---- arch group (mirrors ars-gate.yml) -----------------------------------
# ars-gate.sh scans core/, so an ARS regression can only come from a core/
# change.
if want arch; then
  run_gate_scoped '^core/' "ARS architecture gate" tools/ars-gate.sh
fi

# ---- tools group (mirrors test.yml's "Test the shared shell libs" step) ----
# Scoped to tools/lib/ plus every top-level tools/*.sh rather than naming the
# two current callers: a third script sourcing the lib would otherwise stop
# running these tests without anyone noticing.
shell_lib_tests() {
  local rc=0 t
  for t in tools/lib/*_test.sh; do
    [[ -e "$t" ]] || continue
    bash "$t" || rc=1
  done
  return "$rc"
}

if want tools; then
  # go.work and .github/dependabot.yml are in scope because module-list_test.sh
  # asserts they agree — and a commit touching only one of those two is exactly
  # the commit that breaks the invariant, so leaving them out would skip the
  # test precisely when it matters (#1291).
  # site/install.sh is in the trigger set because tools/lib/install-uninstall_test.sh
  # tests it (#1416). Without it a push touching only the installer would SKIP
  # the one gate that covers the installer.
  run_gate_scoped '^tools/lib/|^tools/[^/]*\.sh$|^go\.work$|^\.github/dependabot\.yml$|^site/install\.sh$' \
                  "tools/lib shell-lib tests" shell_lib_tests
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
  run_gate_scoped '^\.claude/skills/.*\.md$|(^|/)SKILL\.md$|^tools/skill-lint\.sh$' \
                  "skill-file lint" tools/skill-lint.sh
fi

# ---- posix group (mirrors linux.yml's "Lint POSIX sh scripts" step) --------
# The #!/bin/sh corpus is two files today (site/install.sh and
# tools/linux-replay-entrypoint.sh) and the gate re-lints all of them whenever
# it fires — it is a fraction of a second, and the trigger cannot enumerate
# them anyway, because a NEW POSIX script is exactly the file the gate most
# needs to see on the push that adds it. So the regex is deliberately loose:
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
  run_gate_scoped '\.sh$|^(tools|site)/[^/]*$|^tools/git-hooks/|^tools/lib/testdata/posix-lint/' \
                  "POSIX sh lint (#!/bin/sh bashisms)" tools/posix-lint.sh
fi

# ---- security group (mirrors tools/security-scan.sh's local mode; the same
# script's full mode, with GitHub Dependabot/CodeQL alert checks, runs at
# release time from ir:release's Step 5.5, not here) ------------------------
if want security; then
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
# The macOS app had no automated floor of any kind until #1509: no CI workflow
# built or tested Swift, and preflight had no Swift gate either, so a
# platforms/macos-only diff ran *every* gate as SKIP and pushed green having
# checked nothing.
#
# This is the one gate that is deliberately STRONGER than the CI workflow it
# corresponds to, which is the opposite of the parity rule elsewhere in this
# file — so it is worth stating why rather than leaving it to look like drift.
# macos-swift.yml builds and does not test, because the suite does not yet pass
# on a GitHub runner (#1530: 227 tests do, but 35 image-snapshot comparisons are
# host-dependent, one of them hangs, plus #1523 and one home-dependent
# assertion). It DOES pass on a developer Mac, which is exactly where this hook
# runs. So the tests are gated here and nowhere else until #1530 lands; a
# reader who "fixes" this to match the workflow removes the only gate the macOS
# test suite has.
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
  # Both names: `LauncherTestHarness` is the target, `LauncherHarnessTests` the
  # class. Either alone excludes the harness today; the pair is what keeps that
  # true after a class is added to the target or the target is renamed. This
  # matters more locally than in CI — here an unskipped harness test drives the
  # developer's own live terminal windows.
  ( cd platforms/macos && swift build \
      && swift test --skip LauncherTestHarness --skip LauncherHarnessTests )
  return $?
}

if want swift; then
  # macOS-only, and skipped rather than failed elsewhere. The gate mirrors a
  # workflow that is itself `runs-on: macos-latest`, so on Linux it is out of
  # scope, not unmet — without this guard a Linux contributor's plain
  # `tools/preflight.sh` could never go green, since the gate is in the default
  # set and `swift_suite` fails hard on a missing toolchain. The distinction
  # this preserves is the one that matters: "this platform does not run this
  # check" is a SKIP, while "this platform runs it and the tool is missing" is
  # a FAIL.
  if [[ "$(uname -s)" == "Darwin" ]]; then
    run_gate_scoped '^platforms/macos/|^\.github/workflows/macos-swift\.yml$|^tools/preflight\.sh$' \
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

exit "$overall"
