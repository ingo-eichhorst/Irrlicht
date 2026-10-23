#!/usr/bin/env bash
# ars-gate_test.sh — tools/ars-gate.sh must find an `ars` that lives in the Go
# bin dir when that directory is not on PATH, including one it has just
# go-installed there itself (#2099).
#
# ---------------------------------------------------------------------------
# Why this exists
#
# The gate ran `command -v ars || go install …ars@vX`, then called `ars` by
# name. `go install` writes to `go env GOBIN`, or else to the first
# `go env GOPATH` entry's bin/, and nothing put that directory on PATH. CI never
# showed it, because actions/setup-go adds it to PATH. A Factory agent's shell
# does not, so `tools/preflight.sh --only arch` died with
# `tools/ars-gate.sh: line 37: ars: command not found`, exit 127, straight
# after installing ars into /Users/factory/go/bin.
#
# ---------------------------------------------------------------------------
# How it drives the real script
#
# The real tools/ars-gate.sh runs unmodified, with its working directory in a
# throwaway repo. The script takes its REPO_ROOT from `git rev-parse
# --show-toplevel`, so it scans that repo and fetches that repo's `origin`. The
# origin is a real bare repo with a `main` branch, because the gate runs
# `git fetch origin main` and `git worktree add … origin/main`. A bare
# `update-ref` would not satisfy the fetch.
#
# PATH is NOT the caller's PATH. It is a directory of symlinks to the few
# system tools the gate and the stubs need, plus a directory of stubs. A
# developer machine with a real `ars` or GOPATH/bin on its PATH would otherwise
# turn every red-first case below into a lock that passes for the wrong reason.
# The vacuity guard asserts that `ars` does not resolve under that PATH before
# any case runs.
#
# The stub `go` answers `env GOBIN`, `env GOPATH` and `install` the way the real
# one does: install writes to GOBIN, else to the first GOPATH entry's bin/. Every
# call is logged. The stub `ars` logs its own path and argv, then prints a
# diagnostic line and a scan JSON, the shape the gate strips with
# `sed -n '/^{/,$p'`. The cases assert on those logs, never only on what the
# gate failed to print, because "no `command not found` in the output" is also
# what a gate that never reached the scan prints.

set -uo pipefail   # NOT -e: the cases capture the gate's non-zero exits.

REPO_ROOT=$(git rev-parse --show-toplevel)
cd "$REPO_ROOT" || { echo "FAIL: ars-gate_test — cannot cd to $REPO_ROOT" >&2; exit 1; }
GATE="$REPO_ROOT/tools/ars-gate.sh"
[[ -x "$GATE" ]] || { echo "FAIL: ars-gate_test — $GATE is missing or not executable" >&2; exit 1; }

T=$(mktemp -d "${TMPDIR:-/tmp}/ars-gate-test.XXXXXX") || { echo "FAIL: ars-gate_test — mktemp failed" >&2; exit 1; }
trap 'rm -rf "$T"' EXIT

fails=0
pass() { echo "  PASS: $1"; return 0; }
fail() { echo "FAIL: $1 — expected [$2] got [$3]"; fails=$((fails + 1)); return 0; }

# ---------------------------------------------------------------------------
# The scrubbed PATH
#
# A missing tool is a hard failure, not a skip. Exiting 0 here would read as a
# PASS to preflight's shell_lib_tests and to test.yml's loop.
SYSBIN="$T/sysbin"
STUBBIN="$T/stubbin"
mkdir -p "$SYSBIN" "$STUBBIN"
for tool in bash sh env git jq sed mktemp rm cat mkdir chmod cp; do
  src=$(command -v "$tool") || { echo "FAIL: ars-gate_test — $tool not found" >&2; exit 1; }
  ln -s "$src" "$SYSBIN/$tool" || { echo "FAIL: ars-gate_test — cannot link $tool" >&2; exit 1; }
done
SCRUBBED_PATH="$STUBBIN:$SYSBIN"

# The stub ars, written into wherever a case wants an `ars` to exist.
write_stub_ars() {
  cat >"$1" <<'EOF'
#!/bin/sh
printf '%s %s\n' "$0" "$*" >>"$ARS_LOG"
echo "LLM features disabled (stub)"
echo '{"composite_score": 7.5, "categories": [{"name": "stub", "score": 7.0}]}'
EOF
  chmod +x "$1"
}

# The stub go. STUB_INSTALL_NOOP=1 makes `install` succeed without writing
# anything, which is the "installed, still cannot find it" case.
cat >"$STUBBIN/go" <<'EOF'
#!/bin/sh
printf 'go %s\n' "$*" >>"$GO_LOG"
case "$1" in
  env)
    case "$2" in
      GOBIN) printf '%s\n' "${STUB_GOBIN:-}" ;;
      GOPATH) printf '%s\n' "$STUB_GOPATH" ;;
      *) echo "stub go: unexpected env $2" >&2; exit 2 ;;
    esac ;;
  install)
    [ "${STUB_INSTALL_NOOP:-0}" = 1 ] && exit 0
    if [ -n "${STUB_GOBIN:-}" ]; then dir=$STUB_GOBIN; else dir="${STUB_GOPATH%%:*}/bin"; fi
    mkdir -p "$dir" && cp "$STUB_ARS_SRC" "$dir/ars" && chmod +x "$dir/ars" ;;
  *) echo "stub go: unexpected command $1" >&2; exit 2 ;;
esac
EOF
chmod +x "$STUBBIN/go"
STUB_ARS_SRC="$T/ars.src"
write_stub_ars "$STUB_ARS_SRC"

# ---------------------------------------------------------------------------
# Vacuity guard: under the scrubbed PATH there is no ars, and `go` is the stub.
got=$(PATH="$SCRUBBED_PATH" command -v ars || true)
if [[ -n "$got" ]]; then
  echo "FAIL: ars-gate_test — ars resolves under the scrubbed PATH ($got); the cases below would not test anything" >&2
  exit 1
fi
got=$(PATH="$SCRUBBED_PATH" command -v go || true)
if [[ "$got" != "$STUBBIN/go" ]]; then
  echo "FAIL: ars-gate_test — go under the scrubbed PATH is [$got], not the stub" >&2
  exit 1
fi

# ---------------------------------------------------------------------------
# new_case <name> — a throwaway repo whose `origin` is a bare repo carrying
# `main`, and empty logs. Sets CASE_DIR, CASE_REPO, GO_LOG, ARS_LOG.
new_case() {
  CASE_DIR="$T/$1"
  CASE_REPO="$CASE_DIR/work"
  GO_LOG="$CASE_DIR/go.log"
  ARS_LOG="$CASE_DIR/ars.log"
  mkdir -p "$CASE_REPO/core"
  : >"$GO_LOG"
  : >"$ARS_LOG"
  (
    cd "$CASE_REPO" &&
    git init -q -b main . &&
    git config user.email ars-gate-test@example.invalid &&
    git config user.name ars-gate-test &&
    git config commit.gpgsign false &&
    : >core/a.go && git add core/a.go && git commit -qm baseline &&
    git init -q --bare "$CASE_DIR/origin.git" &&
    git remote add origin "$CASE_DIR/origin.git" &&
    git push -q origin main
  ) >/dev/null 2>&1 || { echo "FAIL: ars-gate_test — could not build the scratch repo for $1" >&2; exit 1; }
}

# run_gate — runs the real gate from CASE_REPO under the scrubbed PATH.
# Sets GATE_RC and GATE_OUT.
run_gate() {
  GATE_OUT=$(cd "$CASE_REPO" && env PATH="$SCRUBBED_PATH" GO_LOG="$GO_LOG" ARS_LOG="$ARS_LOG" \
    STUB_ARS_SRC="$STUB_ARS_SRC" STUB_GOBIN="${STUB_GOBIN:-}" STUB_GOPATH="${STUB_GOPATH:-}" \
    STUB_INSTALL_NOOP="${STUB_INSTALL_NOOP:-0}" "$GATE" core 2>&1)
  GATE_RC=$?
}

# expect_scans <label> <ars-path> — the gate exited 0 and the stub at <ars-path>
# ran both scans: the current tree and the origin/main baseline.
expect_scans() {
  local label="$1" ars="$2" n
  if [[ "$GATE_RC" -eq 0 ]]; then pass "$label: gate exited 0"
  else fail "$label: gate exit" "0" "$GATE_RC ($(echo "$GATE_OUT" | tail -3 | tr '\n' ' '))"; fi
  n=$(grep -c "^$ars scan " "$ARS_LOG")
  if [[ "$n" -eq 2 ]]; then pass "$label: $ars ran both scans"
  else fail "$label: scans by $ars" "2" "$n (ars.log: $(tr '\n' ' ' <"$ARS_LOG"))"; fi
}

echo "== ars-gate_test =="

# (a) red-first: ars already sits in GOBIN, which is not on PATH. Unfixed, the
# gate go-installs it again and then cannot call it.
new_case gobin-preinstalled
STUB_GOBIN="$CASE_DIR/gobin"; STUB_GOPATH="$CASE_DIR/gopath"
mkdir -p "$STUB_GOBIN"; write_stub_ars "$STUB_GOBIN/ars"
run_gate
expect_scans "(a) ars in GOBIN, off PATH" "$STUB_GOBIN/ars"
if grep -q '^go install' "$GO_LOG"; then
  fail "(a) ars in GOBIN, off PATH: an ars already in GOBIN is not reinstalled" "no go install" "$(tr '\n' ' ' <"$GO_LOG")"
else
  pass "(a) ars in GOBIN, off PATH: an ars already in GOBIN is not reinstalled"
fi

# (b) red-first: no ars anywhere, GOBIN unset, GOPATH a two-entry list. The
# gate installs it, and `go install` writes to the FIRST GOPATH entry's bin/.
new_case gopath-install
STUB_GOBIN=""; STUB_GOPATH="$CASE_DIR/gopath1:$CASE_DIR/gopath2"
run_gate
expect_scans "(b) installed into first GOPATH entry" "$CASE_DIR/gopath1/bin/ars"
if grep -q '^go install github.com/ingo-eichhorst/agent-readyness/cmd/ars@' "$GO_LOG"; then
  pass "(b) installed into first GOPATH entry: the gate ran go install"
else
  fail "(b) installed into first GOPATH entry: go install" "a go install line" "$(tr '\n' ' ' <"$GO_LOG")"
fi

# (c) lock: ars on PATH is used as before, and `go` is never called. A machine
# with ars on PATH and no Go toolchain must keep working.
new_case ars-on-path
STUB_GOBIN="$CASE_DIR/gobin"; STUB_GOPATH="$CASE_DIR/gopath"
write_stub_ars "$STUBBIN/ars"
run_gate
rm -f "$STUBBIN/ars"
expect_scans "(c) ars on PATH" "$STUBBIN/ars"
if [[ -s "$GO_LOG" ]]; then
  fail "(c) ars on PATH: go is never called" "an empty go.log" "$(tr '\n' ' ' <"$GO_LOG")"
else
  pass "(c) ars on PATH: go is never called"
fi

# (d) red-first: go install reports success but leaves no ars. The gate must
# refuse, name the directory it looked in, and never reach a scan.
new_case install-leaves-nothing
STUB_GOBIN="$CASE_DIR/gobin"; STUB_GOPATH="$CASE_DIR/gopath"; STUB_INSTALL_NOOP=1
run_gate
STUB_INSTALL_NOOP=0
if [[ "$GATE_RC" -ne 0 ]]; then pass "(d) install leaves nothing: gate exited non-zero ($GATE_RC)"
else fail "(d) install leaves nothing: gate exit" "non-zero" "0"; fi
case "$GATE_OUT" in
  *"cannot find it in $STUB_GOBIN"*) pass "(d) install leaves nothing: the refusal names $STUB_GOBIN" ;;
  *) fail "(d) install leaves nothing: refusal message" "cannot find it in $STUB_GOBIN" "$(echo "$GATE_OUT" | tr '\n' ' ')" ;;
esac
if [[ -s "$ARS_LOG" ]]; then
  fail "(d) install leaves nothing: no scan ran" "an empty ars.log" "$(tr '\n' ' ' <"$ARS_LOG")"
else
  pass "(d) install leaves nothing: no scan ran"
fi

if [[ "$fails" -gt 0 ]]; then
  echo "ars-gate_test: $fails failure(s)"
  exit 1
fi
echo "ars-gate_test: all passed"
