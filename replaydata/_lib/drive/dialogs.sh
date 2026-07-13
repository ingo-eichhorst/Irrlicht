#!/usr/bin/env bash
# dialogs.sh — shared TUI-dialog-dismiss poll for the interactive recording
# drivers (#1009). Extracted from drive-mistral-vibe-interactive.sh and
# drive-antigravity-interactive.sh's wait_turn(), whose "is a blocking
# permission dialog on screen right now? dismiss it with Enter" block was
# byte-identical except each adapter's own marker regex and log wording
# (#1003 added the mistral-vibe copy; antigravity's predates it). A third
# adapter needing the same shape would have made it a third copy-paste, so
# this lib owns ONLY the poll+dismiss mechanics — capture the pane, match the
# caller's regex, send Enter. The marker regex and any adapter-specific log
# message stay in the driver, still coupled to that adapter's own
# DetectUI/marker set (core/adapters/inbound/agents/<adapter>) — this lib has
# no opinion on what the dialog says, only on how to poll and dismiss one. The
# same helper also covers antigravity's boot-time "trust this folder" gate in
# launch_repl (a one-shot poll inside a bounded wait, not wait_turn's per-tick
# loop) — the mechanic is identical regardless of which caller loop drives it.
#
# Sourced as a library; MUST NOT call `set` at top level.

# dismiss_dialog_if_visible <session> <marker-regex>
#   One-shot poll (the caller's own loop ticks this, whether that's
#   wait_turn's per-turn poll or a boot-time readiness wait): capture the last
#   ~50 lines of tmux pane <session> and, if they match the extended,
#   case-insensitive <marker-regex>, send a bare Enter — every dialog this
#   guards pre-highlights its accepting choice, so Enter alone dismisses/grants
#   it — and return 0. Returns 1 when the marker isn't currently on screen; the
#   caller decides what to log on a 0 return, since the wording ("dismissed
#   tool-permission dialog" vs "granted agy run_command permission dialog" vs
#   "accepted agy trust-folder prompt") is adapter/call-site-specific context
#   this helper doesn't have.
dismiss_dialog_if_visible() { # <session> <marker-regex>
  local session="$1" regex="$2"
  tmux capture-pane -t "$session" -p -S -50 2>/dev/null | grep -qiE "$regex" || return 1
  tmux send-keys -t "$session" Enter
  return 0
}
