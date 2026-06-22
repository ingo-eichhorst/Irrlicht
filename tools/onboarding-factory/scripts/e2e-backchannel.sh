#!/usr/bin/env bash
# e2e-backchannel.sh — end-to-end verifier for the backchannel-control scenario
# (issue #724). Drives the REAL daemon control stack against a REAL terminal
# backend, locally and through the relay, and reports the terminal-environment
# coverage matrix.
#
# The heavy lifting is the Go integration test TestBackchannelE2E_* (core/cmd/
# irrlichd/backchannel_e2e_test.go): it starts a real `cat` in a tmux pane and
# proves (a) InputService -> Controller -> tmux send-keys delivers locally and
# (b) a relay control frame -> Forwarder -> InputService delivers remotely, both
# verified by capturing the pane. tmux is the only terminal environment that is
# automatable headlessly; kitty, iTerm2 and Terminal.app share the exact same
# InputService/Controller seam (kitty daemon-side via `kitten @ send-text`,
# iTerm2/Terminal.app via the macOS app's AppleScript activators) and are
# covered by unit tests + the per-agent onboarding assessments.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
core="$(cd "$here/../../../core" && pwd)"

echo "== backchannel-control e2e =="
if ! command -v tmux >/dev/null 2>&1; then
  echo "tmux not installed — the automated environment is unavailable; skipping." >&2
  exit 0
fi

echo "-- local + remote against a real tmux pane (Go integration test) --"
( cd "$core" && go test ./cmd/irrlichd/ -run 'TestBackchannelE2E' -count=1 -v )

cat <<'MATRIX'

-- terminal-environment coverage --
  tmux            automated here (send-keys / capture-pane), local + remote
  kitty           same daemon-side seam (kitten @ send-text); manual GUI check
  iTerm2          via macOS app AppleScript activator;        manual GUI check
  Terminal.app    via macOS app AppleScript activator;        manual GUI check

Per-agent support is recorded in the onboarding matrix under scenario
"backchannel-control" (6.1): claudecode/codex/gemini-cli/aider/pi/kiro-cli =
yes; opencode/antigravity = no (no interactive terminal). Run:
  (cd tools/onboarding-factory && go run ./cmd/of status --scenario backchannel-control)
MATRIX
echo "== e2e OK =="
