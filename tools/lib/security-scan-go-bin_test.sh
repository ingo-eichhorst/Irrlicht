#!/usr/bin/env bash
# security-scan-go-bin_test.sh — tools/security-scan.sh must find govulncheck
# and gosec in the Go bin dir when that directory is not on PATH, including a
# govulncheck it has just go-installed there itself (#2099).
#
# ---------------------------------------------------------------------------
# Why this exists
#
# The govulncheck loop ran `command -v govulncheck || go install …@latest`,
# then called `govulncheck` by name. `go install` writes to `go env GOBIN`, or
# else to the first `go env GOPATH` entry's bin/, and nothing put that
# directory on PATH. CI never showed it, because actions/setup-go adds it to
# PATH. On a Factory build host, `tools/security-scan.sh --local` reported all
# eight modules as `scan itself failed … govulncheck: command not found`, with
# govulncheck sitting in /Users/factory/go/bin. A gosec installed the way the
# script's own hint says (`go install …gosec@latest`) landed in the same
# directory and was reported "not found" for the same reason.
# tools/lib/ars-gate_test.sh covers the same defect in tools/ars-gate.sh.
#
# ---------------------------------------------------------------------------
# How it drives the real script
#
# The real tools/security-scan.sh runs unmodified in --local mode against this
# repo. PATH is NOT the caller's PATH. It is a directory of symlinks to the
# system tools the script uses, plus a directory of stubs, so a developer
# machine with govulncheck or gosec on its PATH cannot turn the red-first cases
# into locks. The vacuity guard asserts that neither resolves under that PATH
# before any case runs.
#
# The stub `go` answers `work edit -json` with one module (core), and answers
# `env GOBIN`, `env GOPATH` and `install` the way the real one does: install
# writes to GOBIN, else to the first GOPATH entry's bin/. The stub govulncheck
# prints no finding, so the script reports `OK: govulncheck: core — clean`. The
# stub gosec writes tools/lib/testdata/gosec-report/clean.json to its -out path,
# so the script reports `OK: gosec: core`. Every stub logs its own path and
# argv, and the cases assert on those logs as well as on the OK lines.
# npm is deliberately absent, so the npm audit block reports "npm not found";
# no case reads that, or the script's overall exit status.

set -uo pipefail   # NOT -e: the cases capture the script's non-zero exits.

REPO_ROOT=$(git rev-parse --show-toplevel)
cd "$REPO_ROOT" || { echo "FAIL: security-scan-go-bin_test — cannot cd to $REPO_ROOT" >&2; exit 1; }
SCAN="$REPO_ROOT/tools/security-scan.sh"
GOSEC_REPORT="$REPO_ROOT/tools/lib/testdata/gosec-report/clean.json"
[[ -x "$SCAN" ]] || { echo "FAIL: security-scan-go-bin_test — $SCAN is missing or not executable" >&2; exit 1; }
[[ -s "$GOSEC_REPORT" ]] || { echo "FAIL: security-scan-go-bin_test — $GOSEC_REPORT is missing" >&2; exit 1; }

T=$(mktemp -d "${TMPDIR:-/tmp}/security-scan-go-bin-test.XXXXXX") || { echo "FAIL: security-scan-go-bin_test — mktemp failed" >&2; exit 1; }
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
for tool in bash sh env git jq sed mktemp rm cat mkdir chmod cp tr cut awk grep head tail wc sort uniq basename dirname; do
  src=$(command -v "$tool") || { echo "FAIL: security-scan-go-bin_test — $tool not found" >&2; exit 1; }
  ln -s "$src" "$SYSBIN/$tool" || { echo "FAIL: security-scan-go-bin_test — cannot link $tool" >&2; exit 1; }
done
SCRUBBED_PATH="$STUBBIN:$SYSBIN"

STUB_GOVULNCHECK_SRC="$T/govulncheck.src"
cat >"$STUB_GOVULNCHECK_SRC" <<'EOF'
#!/bin/sh
printf '%s %s\n' "$0" "$*" >>"$TOOL_LOG"
EOF
chmod +x "$STUB_GOVULNCHECK_SRC"

STUB_GOSEC_SRC="$T/gosec.src"
cat >"$STUB_GOSEC_SRC" <<'EOF'
#!/bin/sh
printf '%s %s\n' "$0" "$*" >>"$TOOL_LOG"
for a in "$@"; do
  case "$a" in -out=*) cp "$STUB_GOSEC_REPORT" "${a#-out=}" ;; esac
done
EOF
chmod +x "$STUB_GOSEC_SRC"

# The stub go. STUB_INSTALL_NOOP=1 makes `install` succeed without writing
# anything, which is the "installed, still cannot find it" case.
cat >"$STUBBIN/go" <<'EOF'
#!/bin/sh
printf 'go %s\n' "$*" >>"$GO_LOG"
case "$1" in
  work) echo '{"Use":[{"DiskPath":"./core"}]}' ;;
  env)
    case "$2" in
      GOBIN) printf '%s\n' "${STUB_GOBIN:-}" ;;
      GOPATH) printf '%s\n' "$STUB_GOPATH" ;;
      *) echo "stub go: unexpected env $2" >&2; exit 2 ;;
    esac ;;
  install)
    [ "${STUB_INSTALL_NOOP:-0}" = 1 ] && exit 0
    case "$2" in golang.org/x/vuln/cmd/govulncheck@*) ;; *) echo "stub go: unexpected install $2" >&2; exit 2 ;; esac
    if [ -n "${STUB_GOBIN:-}" ]; then dir=$STUB_GOBIN; else dir="${STUB_GOPATH%%:*}/bin"; fi
    mkdir -p "$dir" && cp "$STUB_GOVULNCHECK_SRC" "$dir/govulncheck" && chmod +x "$dir/govulncheck" ;;
  *) echo "stub go: unexpected command $1" >&2; exit 2 ;;
esac
EOF
chmod +x "$STUBBIN/go"

# ---------------------------------------------------------------------------
# Vacuity guard: under the scrubbed PATH neither tool resolves, and `go` is the
# stub.
for tool in govulncheck gosec; do
  got=$(PATH="$SCRUBBED_PATH" command -v "$tool" || true)
  if [[ -n "$got" ]]; then
    echo "FAIL: security-scan-go-bin_test — $tool resolves under the scrubbed PATH ($got); the cases below would not test anything" >&2
    exit 1
  fi
done
got=$(PATH="$SCRUBBED_PATH" command -v go || true)
if [[ "$got" != "$STUBBIN/go" ]]; then
  echo "FAIL: security-scan-go-bin_test — go under the scrubbed PATH is [$got], not the stub" >&2
  exit 1
fi

# new_case <name> — empty logs. Sets CASE_DIR, GO_LOG, TOOL_LOG.
new_case() {
  CASE_DIR="$T/$1"
  GO_LOG="$CASE_DIR/go.log"
  TOOL_LOG="$CASE_DIR/tools.log"
  mkdir -p "$CASE_DIR"
  : >"$GO_LOG"
  : >"$TOOL_LOG"
}

# run_scan — runs the real script in --local mode under the scrubbed PATH.
# Sets SCAN_OUT.
run_scan() {
  SCAN_OUT=$(env PATH="$SCRUBBED_PATH" GO_LOG="$GO_LOG" TOOL_LOG="$TOOL_LOG" \
    STUB_GOVULNCHECK_SRC="$STUB_GOVULNCHECK_SRC" STUB_GOSEC_REPORT="$GOSEC_REPORT" \
    STUB_GOBIN="${STUB_GOBIN:-}" STUB_GOPATH="${STUB_GOPATH:-}" \
    STUB_INSTALL_NOOP="${STUB_INSTALL_NOOP:-0}" "$SCAN" --local 2>&1)
}

# expect_ran <label> <tool-path> <ok-line> — the stub at <tool-path> ran once,
# and the script printed <ok-line>.
expect_ran() {
  local label="$1" tool="$2" okline="$3" n
  n=$(grep -c "^$tool " "$TOOL_LOG")
  if [[ "$n" -eq 1 ]]; then pass "$label: $tool ran"
  else fail "$label: runs of $tool" "1" "$n (tools.log: $(tr '\n' ' ' <"$TOOL_LOG"))"; fi
  case "$SCAN_OUT" in
    *"$okline"*) pass "$label: the script reported [$okline]" ;;
    *) fail "$label: report line" "$okline" "$(echo "$SCAN_OUT" | grep -E 'govulncheck|gosec' | tr '\n' ' ')" ;;
  esac
}

echo "== security-scan-go-bin_test =="

# (a) red-first: govulncheck and gosec already sit in GOBIN, which is not on
# PATH. Unfixed, the script go-installs govulncheck again and cannot call it,
# and reports gosec not found.
new_case gobin-preinstalled
STUB_GOBIN="$CASE_DIR/gobin"; STUB_GOPATH="$CASE_DIR/gopath"
mkdir -p "$STUB_GOBIN"
cp "$STUB_GOVULNCHECK_SRC" "$STUB_GOBIN/govulncheck"
cp "$STUB_GOSEC_SRC" "$STUB_GOBIN/gosec"
run_scan
expect_ran "(a) tools in GOBIN, off PATH" "$STUB_GOBIN/govulncheck" "OK: govulncheck: core — clean"
expect_ran "(a) tools in GOBIN, off PATH" "$STUB_GOBIN/gosec" "OK: gosec: core"
if grep -q '^go install' "$GO_LOG"; then
  fail "(a) tools in GOBIN, off PATH: a govulncheck already in GOBIN is not reinstalled" "no go install" "$(tr '\n' ' ' <"$GO_LOG")"
else
  pass "(a) tools in GOBIN, off PATH: a govulncheck already in GOBIN is not reinstalled"
fi

# (b) red-first: no govulncheck anywhere, GOBIN unset, GOPATH a two-entry list.
# The script installs it, and `go install` writes to the FIRST GOPATH entry's
# bin/.
new_case gopath-install
STUB_GOBIN=""; STUB_GOPATH="$CASE_DIR/gopath1:$CASE_DIR/gopath2"
run_scan
expect_ran "(b) installed into first GOPATH entry" "$CASE_DIR/gopath1/bin/govulncheck" "OK: govulncheck: core — clean"

# (c) red-first: go install reports success but leaves no govulncheck. The
# script must say where it looked rather than calling a missing command.
new_case install-leaves-nothing
STUB_GOBIN="$CASE_DIR/gobin"; STUB_GOPATH="$CASE_DIR/gopath"; STUB_INSTALL_NOOP=1
run_scan
STUB_INSTALL_NOOP=0
want="FAIL: govulncheck: core — not on PATH or in the Go bin dir ($STUB_GOBIN); go install did not produce it"
case "$SCAN_OUT" in
  *"$want"*) pass "(c) install leaves nothing: the failure names $STUB_GOBIN" ;;
  *) fail "(c) install leaves nothing: failure line" "$want" "$(echo "$SCAN_OUT" | grep govulncheck | tr '\n' ' ')" ;;
esac
if [[ -s "$TOOL_LOG" ]]; then
  fail "(c) install leaves nothing: no scanner ran" "an empty tools.log" "$(tr '\n' ' ' <"$TOOL_LOG")"
else
  pass "(c) install leaves nothing: no scanner ran"
fi

# (d) lock: tools on PATH are used as before, and `go` is asked for nothing
# but the module list.
new_case tools-on-path
STUB_GOBIN="$CASE_DIR/gobin"; STUB_GOPATH="$CASE_DIR/gopath"
cp "$STUB_GOVULNCHECK_SRC" "$STUBBIN/govulncheck"
cp "$STUB_GOSEC_SRC" "$STUBBIN/gosec"
run_scan
rm -f "$STUBBIN/govulncheck" "$STUBBIN/gosec"
expect_ran "(d) tools on PATH" "$STUBBIN/govulncheck" "OK: govulncheck: core — clean"
expect_ran "(d) tools on PATH" "$STUBBIN/gosec" "OK: gosec: core"
other=$(grep -v '^go work edit -json$' "$GO_LOG")
if [[ -n "$other" ]]; then
  fail "(d) tools on PATH: go is asked only for the module list" "only go work edit -json" "$(echo "$other" | tr '\n' ' ')"
else
  pass "(d) tools on PATH: go is asked only for the module list"
fi

# (e) red-first: govulncheck is on PATH, so the govulncheck loop never
# resolves the Go bin dir, and gosec sits only there. Only the gosec loop's
# own go_bin_on_path call can find it, so this is the case that fails if that
# call is dropped.
new_case gosec-only-in-gobin
STUB_GOBIN="$CASE_DIR/gobin"; STUB_GOPATH="$CASE_DIR/gopath"
mkdir -p "$STUB_GOBIN"
cp "$STUB_GOVULNCHECK_SRC" "$STUBBIN/govulncheck"
cp "$STUB_GOSEC_SRC" "$STUB_GOBIN/gosec"
run_scan
rm -f "$STUBBIN/govulncheck"
expect_ran "(e) gosec only in GOBIN" "$STUBBIN/govulncheck" "OK: govulncheck: core — clean"
expect_ran "(e) gosec only in GOBIN" "$STUB_GOBIN/gosec" "OK: gosec: core"

if [[ "$fails" -gt 0 ]]; then
  echo "security-scan-go-bin_test: $fails failure(s)"
  exit 1
fi
echo "security-scan-go-bin_test: all passed"
