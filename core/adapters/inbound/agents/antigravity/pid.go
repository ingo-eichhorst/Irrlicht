package antigravity

import "irrlicht/core/adapters/inbound/agents/processlifecycle"

// DiscoverPID finds the `agy` CLI process owning a session by matching the
// session's working directory against running agy processes. The cwd is the one
// the parser harvests from the transcript's run_command tool calls (state.CWD).
//
// PID binding is optional enrichment for the CLI surface only — it adds process
// liveness and terminal-jump. IDE sessions have no per-conversation process and
// simply never bind (PID stays 0), which transcript-first discovery treats as
// first-class.
//
// agy is a standalone native binary (1 process ↔ 1 conversation), so a plain
// process-name + cwd match suffices — no argv filtering (there is no Node
// launcher/worker split like Gemini CLI). When two sessions share a cwd the
// caller's claimed-aware disambiguator picks the highest unclaimed PID; with no
// disambiguator (nil caller) we fall back to the lowest PID.
func DiscoverPID(cwd, transcriptPath string, disambiguate func([]int) int) (int, error) {
	if disambiguate == nil {
		disambiguate = lowestPID
	}
	return discoverByCWD(ProcessName, cwd, disambiguate)
}

// discoverByCWD is the OS-level process-name + cwd match. A package variable so
// tests can stub it in place of the real pgrep call.
var discoverByCWD = processlifecycle.DiscoverPIDByCWD

// lowestPID picks the smallest PID among matches — the default when the caller
// supplies no claimed-aware disambiguator.
func lowestPID(pids []int) int {
	best := 0
	for _, p := range pids {
		if best == 0 || p < best {
			best = p
		}
	}
	return best
}
