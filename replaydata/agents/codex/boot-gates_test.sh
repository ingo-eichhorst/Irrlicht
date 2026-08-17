#!/usr/bin/env bash
# boot-gates_test.sh — corpus test for boot-gates.sh.
#
# One row per captured pane × every predicate, pinning what each one must match
# AND what it must leave alone. The must-NOT-match half is where the value is:
# four predicates that all fired on everything would satisfy every positive row
# and read as excellent coverage, while sending `2`, `1` and `t` at whatever
# happens to be on screen.
#
# PROVENANCE of the fixtures under testdata/boot-gates/ — five of six are real
# `tmux capture-pane` output from codex-cli 0.147.0, captured during #1388 by a
# probe that pressed keys in a known order and sent no prompt:
#
#   dir-trust.txt             boot, nothing pressed yet
#   hook-menu.txt             after a BARE "1" (no Enter) — which is the
#                             measurement that dir-trust closes on the digit
#   hook-panel-untrusted.txt  after the following Enter selected "> 1. Review hooks"
#   hook-panel-trusted.txt    after "t"; Active flips 0->1 on all three events
#   banner-no-gate.txt        a second probe: cwd pre-trusted, no hooks.json,
#                             so codex boots straight to the composer
#
# update-menu.txt is the exception and says so: this machine's codex is already
# current, so the self-update offer could not be provoked. It is reconstructed
# from the option text driver-interactive.sh recorded when that menu upgraded
# the operator's CLI mid-recording (0.146.1 -> 0.147.0). It is kept as a LOCK on
# the predecessor's case rather than presented as a capture.
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=boot-gates.sh
source "$HERE/boot-gates.sh"
DATA="$HERE/testdata/boot-gates"

fails=0
pass() { printf '  PASS: %s\n' "$1"; }
fail() { printf '  FAIL: %s\n' "$1" >&2; fails=$((fails + 1)); }

# assert_pred <predicate> <fixture> <want: yes|no>
assert_pred() {
  local pred="$1" fixture="$2" want="$3" pane got
  if [[ ! -f "$DATA/$fixture" ]]; then
    fail "$fixture: fixture missing (a corpus that lost its own cases reads as a pass)"
    return 0
  fi
  pane="$(cat "$DATA/$fixture")"
  if "$pred" "$pane"; then got=yes; else got=no; fi
  if [[ "$got" == "$want" ]]; then
    pass "$pred($fixture) = $want"
  else
    fail "$pred($fixture) = $got, want $want"
  fi
  return 0
}

echo "== every pane is classified by exactly the predicate that owns it =="
#            predicate                        dir-trust  hook-menu  panel-untrusted  panel-trusted  banner  update
#            ---------                        ---------  ---------  ---------------  -------------  ------  ------
assert_pred codex_pane_has_dir_trust          dir-trust.txt              yes
assert_pred codex_pane_has_hook_menu          dir-trust.txt              no
assert_pred codex_pane_has_hook_panel         dir-trust.txt              no
assert_pred codex_pane_has_update_menu        dir-trust.txt              no

assert_pred codex_pane_has_hook_menu          hook-menu.txt              yes
assert_pred codex_pane_has_dir_trust          hook-menu.txt              no
assert_pred codex_pane_has_hook_panel         hook-menu.txt              no
assert_pred codex_pane_has_update_menu        hook-menu.txt              no

assert_pred codex_pane_has_hook_panel         hook-panel-untrusted.txt   yes
assert_pred codex_pane_has_hook_menu          hook-panel-untrusted.txt   no
assert_pred codex_pane_has_dir_trust          hook-panel-untrusted.txt   no
assert_pred codex_pane_has_update_menu        hook-panel-untrusted.txt   no

assert_pred codex_pane_has_update_menu        update-menu.txt            yes
assert_pred codex_pane_has_dir_trust          update-menu.txt            no
assert_pred codex_pane_has_hook_menu          update-menu.txt            no
assert_pred codex_pane_has_hook_panel         update-menu.txt            no

echo ""
echo "== the two panes that need NO answer are matched by no gate predicate =="
# The vacuity guard. Without these rows, predicates that returned true
# unconditionally would pass every row above.
for fixture in banner-no-gate.txt hook-panel-trusted.txt; do
  assert_pred codex_pane_has_update_menu "$fixture" no
  assert_pred codex_pane_has_dir_trust   "$fixture" no
  assert_pred codex_pane_has_hook_menu   "$fixture" no
  assert_pred codex_pane_has_hook_panel  "$fixture" no
done

echo ""
echo "== 'trusted' is distinguished from 'still asking', both directions =="
# The discriminator the driver's confirm loop depends on. A predicate that read
# only "the panel is on screen" would report the untrusted panel as done and let
# the run continue with hooks disabled — the #1388 failure with an extra step.
assert_pred codex_pane_hook_panel_is_trusted hook-panel-trusted.txt   yes
assert_pred codex_pane_hook_panel_is_trusted hook-panel-untrusted.txt no
assert_pred codex_pane_hook_panel_is_trusted hook-menu.txt            no
assert_pred codex_pane_hook_panel_is_trusted banner-no-gate.txt       no

echo ""
echo "== a SCROLLBACK capture holding both footers is not reported trusted =="
# hook-panel-scrollback-both.txt is the two real captures concatenated, which is
# what `capture-pane -S -N` returns once the panel has redrawn in place: #1388's
# own run produced a `-S -15` dump carrying the untrusted panel twice, so
# accumulating redraws is observed, not assumed.
#
# This row is the whole reason codex_pane_hook_panel_is_trusted carries a
# NEGATIVE clause. Its positive clause alone is true here — the trusted footer is
# present — so a predicate without the negation reports "trusted" off a stale
# frame. Erring toward "still asking" costs a retry and a warning; erring the
# other way is a silent zero-hook recording.
assert_pred codex_pane_hook_panel_is_trusted hook-panel-scrollback-both.txt no
# And the DETECTION predicate deliberately still fires on it: it cannot tell a
# stale frame from a live one, which is exactly why the driver's branches are
# fire-once and why its confirm read is the live screen (no -S) rather than
# scrollback. Pinned so that limitation is learned from a test, not an incident.
assert_pred codex_pane_has_hook_panel hook-panel-scrollback-both.txt yes

echo ""
echo "== the untrusted panel's own evidence is in the fixture (anti-rot) =="
# A fixture that quietly stopped containing the thing it was captured for would
# make every row above pass for the wrong reason.
if grep -q 'hooks need review before they can run' "$DATA/hook-panel-untrusted.txt"; then
  pass "hook-panel-untrusted.txt still carries the untrusted warning it was captured for"
else
  fail "hook-panel-untrusted.txt no longer carries 'hooks need review before they can run'"
fi
if grep -qE '^ +(PermissionRequest|PostToolUse|Stop) +1 +1 ' "$DATA/hook-panel-trusted.txt"; then
  pass "hook-panel-trusted.txt still shows all three irrlicht events Installed=1 Active=1"
else
  fail "hook-panel-trusted.txt no longer shows the three events as Active"
fi

echo ""
if [[ "$fails" -eq 0 ]]; then
  echo "boot-gates_test: ALL PASS"
else
  echo "boot-gates_test: $fails FAILURE(S)" >&2
fi
exit "$fails"
