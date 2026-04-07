package claudecode

import "irrlicht/core/domain/agent"

// StatePolicy returns Claude Code-specific state behavior.
//
// The stale-tool-call timer is disabled because Claude Code's transcript
// format can't distinguish a permission-pending modal from a long-running
// tool invocation (e.g. a multi-minute Bash build). On long sessions this
// produced dozens of spurious working→waiting flips per hour — see issue
// #102 and the replay harness in core/cmd/replay-session. Pi already
// disables the timer for the same reason.
//
// If Claude Code ever surfaces a deterministic permission-pending signal
// (e.g. via a Notification hook writing a marker line to the transcript),
// we can re-enable this and feed off that signal instead of wall-clock gaps.
func StatePolicy() agent.StatePolicy {
	return agent.StatePolicy{EnableStaleToolTimer: false}
}
