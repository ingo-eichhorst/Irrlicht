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
# Usage:
#   tools/preflight.sh                 # everything except the Linux gate
#   tools/preflight.sh --linux         # + full Linux parity via Docker
#   tools/preflight.sh --only go       # just the go-test.yml-equivalent gates
#   tools/preflight.sh --only web      # just the two npm test trees
#   tools/preflight.sh --only arch     # just the ARS architecture gate
#   tools/preflight.sh --only linux    # just the Linux Docker gate
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
while [[ $# -gt 0 ]]; do
  case "$1" in
    --linux) RUN_LINUX=1; shift ;;
    --only)  ONLY="${2:-}"; shift 2 ;;
    -h|--help) sed -n '2,24p' "$0"; exit 0 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done
[[ "$ONLY" == "linux" ]] && RUN_LINUX=1

want() { [[ -z "$ONLY" || "$ONLY" == "$1" ]]; }

NAMES=()
RESULTS=()
overall=0

run_gate() {
  local name="$1"; shift
  echo
  echo "=============================================================="
  echo "  $name"
  echo "=============================================================="
  if "$@"; then
    NAMES+=("$name"); RESULTS+=("PASS")
  else
    NAMES+=("$name"); RESULTS+=("FAIL")
    overall=1
  fi
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

if want go; then
  run_gate "gofmt"                    gofmt_check
  run_gate "core module tests"        go test ./core/... -race -count=1
  run_gate "onboarding-factory tests" go test ./tools/onboarding-factory/... -count=1
  run_gate "replaydata validate"      go run ./tools/onboarding-factory/cmd/of validate
  run_gate "recording-rig smoke test" bash tools/onboarding-factory/scripts/smoke-test.sh
fi

# ---- web group (mirrors web-test.yml) -----------------------------------
web_tree() { ( cd "$1" && npm ci && npm test ); }

if want web; then
  run_gate "web: platforms/web"             web_tree platforms/web
  run_gate "web: onboarding-factory viewer" web_tree tools/onboarding-factory/internal/viewer/web
fi

# ---- arch group (mirrors ars-gate.yml) -----------------------------------
if want arch; then
  run_gate "ARS architecture gate" tools/ars-gate.sh
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
echo "=============================================================="
echo "  summary"
echo "=============================================================="
for i in "${!NAMES[@]}"; do
  printf "  %-58s %s\n" "${NAMES[$i]}" "${RESULTS[$i]}"
done

exit "$overall"
