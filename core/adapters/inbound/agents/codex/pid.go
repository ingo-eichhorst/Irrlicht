package codex

import (
	"irrlicht/core/adapters/inbound/agents/processlifecycle"
)

// DiscoverPID finds the Codex process owning a session by checking which
// process has the transcript file open for writing. Codex keeps transcript
// files open during the session lifetime, unlike Claude Code which opens,
// writes, and closes.
//
// Since codex ~0.162 the process holding a TUI's rollout open is usually not
// the TUI: every TUI attaches over a unix socket to one shared managed
// app-server daemon (`codex app-server … --managed-daemon`), which writes all
// rollouts — so every live root resolves to the daemon's PID. Observed on the
// dev machine for #2077 (2026-10-10) by counting `rollout-*.jsonl` lines in
// `lsof -p <pid>` for each `pgrep -x codex` PID: the 0.162.1 daemon held 4,
// each of the three `codex --yolo` TUIs held 0. OwnsSharedPID is what keeps
// those roots from superseding each other.
func DiscoverPID(cwd, transcriptPath string, disambiguate func([]int) int) (int, error) {
	return processlifecycle.DiscoverPIDByTranscriptWriter(transcriptPath)
}

// OwnsSharedPID reports whether this rollout is still held open for writing by
// pid — the same transcript-writer probe DiscoverPID uses, asked of one root
// that shares pid with a newer codex root. An erroring probe, no writer, or a
// different writer does not confirm ownership (agent.SharedPIDOwnerFunc's
// contract: inconclusive is false), so the root falls back to the exclusive
// same-PID policy and is retired.
//
// When the answer turns false for a root the daemon still hosts is codex's
// thread unload, read in codex source at tag rust-v0.162.1 and NOT observed
// live (#2077): app-server/src/request_processors/thread_lifecycle.rs
// (UnloadingState) shuts a thread down once it has no subscribers and has been
// inactive for thread_unload_delay, which core/src/config/mod.rs defaults to
// 60s. So a root left behind by /new is expected to keep the daemon's PID
// until that unload, and to be retired by the next same-PID sweep after it —
// not within milliseconds of the new root's PID binding, as the exclusive
// policy did. That holds only for a root that is not the NEWEST on the PID:
// the same-PID paths never probe their winner (isDedupDeleteCandidate in
// pid_manager.go skips it), so a released newest root — e.g. the last TUI
// opened, then closed — stays until a newer codex root binds the PID or the
// daemon exits (#2077's named residual).
func OwnsSharedPID(cwd, transcriptPath string, pid int) bool {
	if pid <= 0 {
		return false
	}
	owner, err := DiscoverPID(cwd, transcriptPath, nil)
	return err == nil && owner == pid
}
