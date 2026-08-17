#!/usr/bin/env bash
# boot-gates.sh — the four things codex can put on screen before it will accept
# a prompt, as named predicates over a captured tmux pane.
#
# Sourced by driver-interactive.sh; it MUST NOT call `set` at top level.
#
# WHY THIS IS A LIB AND NOT FOUR INLINE greps (#1388)
#
# The driver's boot poll used to grep four literals inline. A literal that stops
# matching does not fail: the gate stays on screen, the poll times out, the
# prompt is typed INTO the gate, and the run produces a completely healthy-looking
# recording that fires zero hooks. That is the exact failure #1388 exists to
# close, and it happened again on codex 0.147.0 during #1388's own recording run
# — so the strings now live behind predicates with a committed corpus of real
# captured panes (testdata/boot-gates/, boot-gates_test.sh). A string that stops
# matching upstream is then a red test, not a silent zero-hook fixture.
#
# THE GATES, in the order codex presents them (0.147.0, captured live)
#
#   1. self-update  "1. Update now (runs `npm install -g @openai/codex`)"
#      Answer 2 (Skip). Unanswered its default upgrades the operator's CLI
#      mid-recording — observed doing exactly that during #1388, 0.146.1 ->
#      0.147.0, invalidating the agent_cli_version the run was about to stamp.
#
#   2. directory trust  "Do you trust the contents of this directory?"
#      Answer 1. **The bare digit dismisses it — no Enter.** Measured: a probe
#      that sent ONLY "1" saw the dialog replaced by gate 3 immediately (no
#      keystroke followed for 3.6s). This is load-bearing, see below.
#
#   3. hook trust MENU  "2. Trust all and continue"
#      Answer 2 + Enter. Still present on 0.147.0 — it did not go away, it
#      merely became reachable-past.
#
#   4. hook trust PANEL  "Press t to trust all; enter to review hooks"
#      Answer t, then Escape to close. This is the review screen BEHIND option 1
#      of gate 3, and it is where a run lands when a stray Enter selects the
#      preselected "> 1. Review hooks".
#
# HOW #1388's OWN RUN FAILED, which is why gates 2 and 4 are one story
#
# The driver answered gate 2 with "1", slept 0.3s, then sent Enter. Gate 2 had
# already closed on the digit, so that Enter landed on gate 3 — whose preselected
# row is "> 1. Review hooks" — opening gate 4, which no predicate recognized. The
# poll then timed out, the first step_send typed its prompt into the panel, the
# "t" in the word "Create" trusted the hooks by accident, and Enter opened the
# per-hook detail view. Zero prompts reached codex, zero rollouts were written,
# and driver.exit-reason said "ok".
#
# So the digit-only answer is not a micro-optimization; it is what stops a run
# walking past the hook gate. It is still sent defensively (see the driver): the
# Enter is only sent if the dialog is STILL up, so an older codex that needs it
# keeps working.

# codex_pane_has_update_menu <pane-text>
# Matches the MENU's own line, not the "Update available!" notice, which codex
# also prints non-interactively where there is nothing to answer.
codex_pane_has_update_menu() {
  grep -q 'Update now' <<<"${1:-}"
}

# codex_pane_has_dir_trust <pane-text>
codex_pane_has_dir_trust() {
  grep -q 'Do you trust' <<<"${1:-}"
}

# codex_pane_has_hook_menu <pane-text>
codex_pane_has_hook_menu() {
  grep -q 'Trust all and continue' <<<"${1:-}"
}

# codex_pane_has_hook_panel <pane-text>
# TRUE only while the panel still has something to trust. The trusted panel is
# deliberately NOT a match: it needs no answer, and treating it as a gate would
# make the driver send `t` at a screen where `t` is not a shortcut — i.e. type a
# literal "t" into whatever has focus.
codex_pane_has_hook_panel() {
  grep -q 'Press t to trust all' <<<"${1:-}"
}

# codex_hook_trust_answer <pane-text> prints which hook gate a pane needs
# answered — `panel`, `menu`, or `none` — resolving the one case where both
# predicates fire at once.
#
# That case is normal, not exotic. The driver polls a 40-line SCROLLBACK read,
# and the panel is only ever reachable THROUGH the menu, so a pane that has
# reached the panel still carries the menu's text above it. The reverse cannot
# happen. So the later screen wins: `panel` outranks `menu`, always.
#
# The precedence lives here rather than in the order of the driver's if/elif
# chain because getting it backwards is silent — it would send the menu's "2"
# at a screen where 2 is not a choice, typing a literal 2 into the panel while
# the gate stays open and the run goes on to record zero hooks.
codex_hook_trust_answer() {
  local pane="${1:-}"
  if codex_pane_has_hook_panel "$pane"; then
    printf 'panel\n'
  elif codex_pane_has_hook_menu "$pane"; then
    printf 'menu\n'
  else
    printf 'none\n'
  fi
}

# codex_pane_hook_panel_is_trusted <pane-text>
# The panel is up and every entry is trusted: the footer has dropped its
# trust-all affordance and offers only review/close. Used to confirm that a `t`
# actually landed, rather than assuming the keystroke was delivered — codex
# swallows input during boot, and the trusted_hash it writes to config.toml is
# NOT written at trust time (a probe found the [hooks] section still absent 5s
# after the panel reported every entry Active), so the config cannot serve as
# the confirmation here.
codex_pane_hook_panel_is_trusted() {
  local pane="${1:-}"
  grep -q 'Press enter to view hooks' <<<"$pane" &&
    ! grep -q 'Press t to trust all' <<<"$pane"
}
