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
#                                      of validate, recording-rig smoke test
#   .github/workflows/web-test.yml  — npm test in both web trees
#   .github/workflows/ars-gate.yml  — ARS architecture-regression gate
#                                      (composite/category score vs origin/main)
#   .github/workflows/linux.yml     — build + full test suite (-race) +
#                                      replay-fixtures under Linux, via the
#                                      linux-replay Docker image (opt-in:
#                                      needs Docker, by far the slowest gate)
#
# The `security` group has no matching workflow — GitHub Actions doesn't run
# it yet (see .claude/skills/ir:release/SKILL.md's Step 5.5, which runs the
# same tools/security-scan.sh at release time). It's local-only for now
# because govulncheck/gosec/npm audit cost real time (~1 minute) and are
# more valuable as a pre-push gate than on every PR push; a GH Actions
# equivalent can be added later without changing tools/security-scan.sh.
#
# Usage:
#   tools/preflight.sh                 # everything except the Linux gate
#   tools/preflight.sh --linux         # + full Linux parity via Docker
#   tools/preflight.sh --only go       # just the go-test.yml-equivalent gates
#   tools/preflight.sh --only web      # just the two npm test trees
#   tools/preflight.sh --only arch     # just the ARS architecture gate
#   tools/preflight.sh --only security # just govulncheck + gosec + npm audit
#   tools/preflight.sh --only linux    # just the Linux Docker gate
#   tools/preflight.sh --changed       # scope every gate to the packages/trees
#                                        this branch changes vs origin/main —
#                                        used by the pre-push hook so a small
#                                        push finishes in seconds. Without it
#                                        the full run above is unchanged.
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
    -h|--help) sed -n '2,24p' "$0"; exit 0 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done
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
CHANGED_FILES=""
if [[ "$CHANGED" == 1 ]]; then
  base=$(git merge-base origin/main HEAD 2>/dev/null || echo origin/main)
  CHANGED_FILES=$(
    { git diff --name-only "$base" HEAD
      git diff --name-only HEAD
      git diff --name-only --cached
    } 2>/dev/null | sort -u
  )
fi

# changed_matches <extended-regex> — true when NOT scoping (full run) or when
# some changed file matches. Lets a full-mode gate stay unconditional.
changed_matches() {
  [[ "$CHANGED" == 1 ]] || return 0
  grep -qE "$1" <<<"$CHANGED_FILES"
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
  if ! changed_matches "$re"; then
    echo
    echo "$SEPARATOR"
    echo "  $1  — SKIP (no changed files match)"
    echo "$SEPARATOR"
    NAMES+=("$1"); RESULTS+=("SKIP")
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
  run_gate_scoped '^tools/onboarding-factory/' \
                  "recording-rig smoke test" bash tools/onboarding-factory/scripts/smoke-test.sh
  run_gate_scoped '^tools/starhistory/' \
                  "starhistory tests"        go test ./tools/starhistory/... -count=1
fi

# ---- web group (mirrors web-test.yml) -----------------------------------
web_tree() {
  local dir="$1"
  ( cd "$dir" && npm ci && npm test )
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

# ---- security group (mirrors tools/security-scan.sh's local mode; the same
# script's full mode, with GitHub Dependabot/CodeQL alert checks, runs at
# release time from ir:release's Step 5.5, not here) ------------------------
if want security; then
  run_gate_scoped '\.go$|(^|/)go\.(mod|sum)$|(^|/)package(-lock)?\.json$' \
                  "security scan (govulncheck + gosec + npm audit)" tools/security-scan.sh --local
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
for i in "${!NAMES[@]}"; do
  printf "  %-58s %s\n" "${NAMES[$i]}" "${RESULTS[$i]}"
done

exit "$overall"
