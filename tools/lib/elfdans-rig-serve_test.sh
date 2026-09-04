#!/usr/bin/env bash
# elfdans-rig-serve_test.sh — grades tools/elfdans-rig.sh's Tailscale publish
# step. Plain bash, no framework, matching tools/lib/await-gone_test.sh. Run
# directly, or via tools/preflight.sh's `tools` gate.
#
# The defect: `tailscale serve --bg` does not fail when Serve is disabled for
# the tailnet. It prints an enable link and then polls until somebody clicks
# it, so `up --serve` — whose output the caller pipes — hung with the relay
# already started and nothing on screen to say why. Observed on a real tailnet;
# the run sat there until it was killed by hand.
#
# The function under test is EXTRACTED from the script rather than reached
# through it, the shape tools/lib/gist-badge-guards_test.sh uses for a workflow
# step's `run:` block: `up` builds a relay and issues a token before it ever
# reaches the publish step, and none of that is what this file grades.
#
# ---------------------------------------------------------------------------
# LOCKS versus red-first evidence
#
# Everything here grades behaviour this change ADDS, so nothing below ran red
# before the fix in the ordinary sense — the function did not exist. The
# evidence is a mutation instead, per AGENTS.md: delete the deadline branch
# from serve_over_tailscale and TEST 1 stops failing the run and starts hanging
# it, which is the defect restored exactly.
#
# `stub_tailscale hangs` is that mutation's fixture, kept here rather than
# described in a PR body, so the next person can re-run the evidence.

set -uo pipefail   # NOT -e: assertions capture non-zero return codes

DIR="$(cd "$(dirname "$0")" && pwd)"
RIG="$DIR/../elfdans-rig.sh"

# A missing tool is a hard failure, not a skip: exiting 0 here would read as a
# PASS to preflight's shell_lib_tests, so the gate would go green having
# asserted nothing.
need() { command -v "$1" >/dev/null 2>&1 || { echo "FAIL: elfdans-rig-serve_test — $1 not found" >&2; exit 1; }; }
need sed
need sleep
[[ -r "$RIG" ]] || { echo "FAIL: cannot read $RIG" >&2; exit 1; }

fails=0
ok()   { printf '  ok   %s\n' "$*"; }
bad()  { printf '  FAIL %s\n' "$*" >&2; fails=$(( fails + 1 )); }

# ── extract the subject ────────────────────────────────────────────────────
# Extraction that silently matches nothing would let every assertion below
# "pass" against an empty function, so the text is checked before it is eval'd.
SRC="$(sed -n '/^serve_over_tailscale() {/,/^}/p' "$RIG")"
if ! grep -q 'SERVE_WAIT_SECONDS' <<<"$SRC"; then
    echo "FAIL: could not extract serve_over_tailscale from $RIG (renamed?)" >&2
    exit 1
fi

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# The rig's own helpers, reduced to what the subject calls. shellcheck cannot
# see through the `eval "$SRC"` below, so every one of these reads as unused.
# shellcheck disable=SC2034  # read by the eval'd serve_over_tailscale
RIG_DIR="$WORK"
# shellcheck disable=SC2034  # read by the eval'd serve_over_tailscale
PORT=7839
SERVE_WAIT_SECONDS=2
# The real script's die EXITS. A stub that merely returned would let the
# function run on past its own refusal, so the subject is invoked in a
# subshell below and this exits, exactly as it does in production.
die()  { printf 'rig-die: %s\n' "$*"; exit 1; }
info() { :; }
eval "$SRC"

# ── stubs ──────────────────────────────────────────────────────────────────
# Each writes the shape the real command produces, so an assertion about the
# operator seeing the reason is an assertion about real output.
stub_tailscale() {
    local mode="$1"
    cat > "$WORK/tailscale" <<EOF
#!/usr/bin/env bash
if [[ "\${2:-}" = "status" || "\${1:-}" = "status" ]]; then echo "serve status"; exit 0; fi
case "$mode" in
  hangs)
    echo "Serve is not enabled on your tailnet."
    echo "To enable, visit:"
    echo "         https://login.tailscale.com/f/serve?node=STUB"
    sleep 3600 ;;
  works)
    echo "Available within your tailnet:"
    echo "https://stub.example.ts.net/" ;;
  fails)
    echo "some other failure" >&2; exit 3 ;;
esac
EOF
    chmod +x "$WORK/tailscale"
}
export PATH="$WORK:$PATH"

# elapsed_ms runs a command and reports how long it took, so "returned" and
# "returned promptly" are different assertions.
elapsed_ms() {
    local start end
    start=$(date +%s)
    ( "$@" ) >"$WORK/out" 2>&1; local rc=$?
    end=$(date +%s)
    ELAPSED=$(( end - start ))
    return "$rc"
}

# ── TEST 1 — the defect ────────────────────────────────────────────────────
# A tailscale that polls forever must become a bounded, explained failure.
# Without the deadline branch this line never returns, which is the bug.
stub_tailscale hangs
if elapsed_ms serve_over_tailscale; then
    bad "a hanging tailscale reported success"
else
    if (( ELAPSED > SERVE_WAIT_SECONDS + 8 )); then
        bad "waited ${ELAPSED}s for a ${SERVE_WAIT_SECONDS}s bound — the deadline is not holding"
    else
        ok "a hanging tailscale fails in ${ELAPSED}s instead of hanging"
    fi
    if grep -q 'login.tailscale.com/f/serve' "$WORK/out"; then
        ok "the enable link reaches the operator"
    else
        bad "the enable link was swallowed — the operator gets a failure with no fix"
    fi
    if grep -qi 'Serve is not enabled' "$WORK/out"; then
        ok "tailscale's own reason is shown verbatim"
    else
        bad "tailscale's reason was swallowed"
    fi
fi

# ── TEST 2 — the happy path is not slowed by the bound ─────────────────────
stub_tailscale works
if elapsed_ms serve_over_tailscale; then
    if (( ELAPSED >= SERVE_WAIT_SECONDS )); then
        bad "a serve that returned at once still cost ${ELAPSED}s — the wait is a sleep, not a poll"
    else
        ok "a working serve returns promptly (${ELAPSED}s)"
    fi
    grep -q 'stub.example.ts.net' "$WORK/out" \
        && ok "the published URL is shown" \
        || bad "the published URL was swallowed"
else
    bad "a working tailscale was reported as a failure"
fi

# ── TEST 3 — a real error is still an error ────────────────────────────────
stub_tailscale fails
if elapsed_ms serve_over_tailscale; then
    bad "a tailscale exiting 3 was reported as success"
else
    ok "a non-zero exit is still a failure"
fi

# ── TEST 4 — teardown targets the port serve actually publishes on ─────────
# `serve --bg <localport>` publishes on 443, so `--https=$PORT off` matched no
# handler and left the tailnet publication up. A lock on the corrected form.
if grep -q 'tailscale serve --https="\$SERVE_HTTPS_PORT" off' "$RIG"; then
    ok "down turns off the published HTTPS port, not the local one"
else
    bad "down's teardown does not use SERVE_HTTPS_PORT — it will leave the tailnet publication up"
fi

if (( fails > 0 )); then
    printf '\nelfdans-rig-serve_test: %d failure(s)\n' "$fails" >&2
    exit 1
fi
printf '\nelfdans-rig-serve_test: all assertions passed\n'
