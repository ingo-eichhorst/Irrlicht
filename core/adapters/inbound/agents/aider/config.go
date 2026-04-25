package aider

import (
	"irrlicht/core/adapters/inbound/agents"
	"irrlicht/core/pkg/tailer"
)

// Config returns the registration record the daemon uses to wire this adapter.
//
// Aider is registered as a stub for the post-discovery live-recording smoke
// (see .claude/skills/ir:onboard-agent/discovery-instructions.md). The parser
// is a no-op because Aider's transcript format is markdown, not JSONL — the
// tailer's JSON-line gate filters every line out before reaching the parser.
// This stub exists to make the daemon's process scanner detect `aider` and
// emit `pid_discovered` lifecycle events; full parser work is a follow-up.
func Config() agents.Config {
	return agents.Config{
		Name:        AdapterName,
		ProcessName: ProcessName,
		RootDir:     rootDir,
		NewParser:   func() tailer.TranscriptParser { return &NoOpParser{} },
		DiscoverPID: DiscoverPID,
		// Aider's actual OS process is `python` invoking the aider script
		// (uv/pipx wrapper), so `pgrep -x aider` finds nothing. Match the
		// binary path on the command line instead. The leading slash anchors
		// to the binary path and excludes wrappers (tmux, sh) that mention
		// `aider` in their own argv.
		CommandLineMatch: "/aider",
	}
}
