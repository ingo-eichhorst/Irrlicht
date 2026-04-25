package aider

import (
	"irrlicht/core/adapters/inbound/agents"
	"irrlicht/core/pkg/tailer"
)

// Config returns the registration record the daemon uses to wire this adapter.
//
// Aider's transcript is markdown (`.aider.chat.history.md`), not JSONL.
// The parser implements tailer.RawLineParser so the tailer skips its JSON
// pre-parse and feeds each trimmed line directly. The post-discovery
// lifecycle (presession_created, pid_discovered, transcript_new,
// transcript_activity, process_exited, transcript_removed) was wired in
// #211; this Config closes the loop by emitting structured ParsedEvents
// from the markdown content.
func Config() agents.Config {
	return agents.Config{
		Name:        AdapterName,
		ProcessName: ProcessName,
		RootDir:     rootDir,
		NewParser:   func() tailer.TranscriptParser { return &Parser{} },
		DiscoverPID: DiscoverPID,
		// Aider's actual OS process is `python` invoking the aider script
		// (uv/pipx wrapper), so `pgrep -x aider` finds nothing. Match the
		// binary path on the command line instead. The leading slash anchors
		// to the binary path and excludes wrappers (tmux, sh) that mention
		// `aider` in their own argv.
		CommandLineMatch: "/aider",
		// Aider writes its chat history per-project (in CWD), not under
		// ~/.aider. The scanner probes each detected aider process's CWD
		// for this file and emits transcript_new with the real path when
		// the file appears (lazily, on the first user message).
		TranscriptFilename: ".aider.chat.history.md",
	}
}
