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
// pid — the question DiscoverPID answers by scanning every process, asked
// instead of pid alone (processlifecycle.HoldsForWriting, #2079), for one root
// that shares pid with a newer codex root. A probe that could not run, or a
// pid that does not hold the rollout, does not confirm ownership
// (agent.SharedPIDOwnerFunc's contract: inconclusive is false), so the root
// falls back to the exclusive same-PID policy and is retired.
//
// What turns the answer false for a root the daemon still hosts is codex's
// thread unload, read in codex source at tag rust-v0.162.1 and NOT observed
// live (#2077): app-server/src/request_processors/thread_lifecycle.rs
// (UnloadingState) shuts a thread down once it has no subscribers and has been
// inactive for thread_unload_delay, which core/src/config/mod.rs defaults to
// 60s. So a root left behind by /new is expected to keep the daemon's PID
// until that unload, and to be retired by the next same-PID sweep after it —
// not within milliseconds of the new root's PID binding, as the exclusive
// policy did. The same-PID paths never ask about their winner (the root
// being assigned, or the newest root on the PID for the startup and periodic
// sweeps); see isDedupDeleteCandidate in core/application/services/pid_manager.go.
// ReleasedPID below is what ends that winner once its rollout is released
// (#2080).
func OwnsSharedPID(cwd, transcriptPath string, pid int) bool {
	held, err := processlifecycle.HoldsForWriting(pid, transcriptPath)
	return err == nil && held
}

// ReleasedPID reports that pid verifiably no longer holds this rollout open
// for writing (#2080): the per-pid probe ran and answered "does not hold"
// (processlifecycle.HoldsForWriting's (false, nil)). A probe that could not
// run answers false, as does a rollout still held, so neither ends a session.
//
// The guard comes first because HoldsForWriting answers (false, nil) for a
// non-positive pid or an empty path, which name nothing that could hold a
// file. That is a safe "no" for OwnsSharedPID and the opposite here: passed
// through, it would read as "released" and end a session bound to no process,
// or a pre-session that has no rollout yet.
//
// What releases the rollout is the thread unload described on OwnsSharedPID
// (codex rust-v0.162.1 thread_lifecycle.rs, read in source and NOT observed
// live): closing the TUI drops the thread's last subscription, so the root is
// expected to end about thread_unload_delay (60s by default) after the TUI
// exits, while the daemon — the PID the root is bound to — lives on.
func ReleasedPID(cwd, transcriptPath string, pid int) bool {
	if pid <= 0 || transcriptPath == "" {
		return false
	}
	held, err := processlifecycle.HoldsForWriting(pid, transcriptPath)
	return err == nil && !held
}
