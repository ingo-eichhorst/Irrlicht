package opencode

import (
	"irrlicht/core/adapters/inbound/agents"
	"irrlicht/core/domain/session"
	"irrlicht/core/pkg/tailer"
)

// Config returns the registration record the daemon uses to wire this adapter.
//
// OpenCode stores session state in a SQLite database rather than JSONL files.
// Two deviations from the standard adapter pattern:
//
//  1. RootDir is set to the XDG data path containing the DB so the process
//     scanner still picks up opencode processes by CWD. The fswatcher that
//     main.go creates from RootDir will watch the DB directory but won't find
//     any .jsonl files — that's expected and harmless; the real session
//     discovery happens through the dedicated Watcher registered separately
//     in main.go.
//
//  2. ComputeMetrics is overridden with a SQLite-based provider that queries
//     the `part` table directly, bypassing the JSONL tailer.
func Config() agents.Config {
	return agents.Config{
		Name:        AdapterName,
		ProcessName: ProcessName,
		RootDir:     ".local/share/opencode", // DB lives here; fswatcher watches dir but ignores non-.jsonl
		NewParser:   func() tailer.TranscriptParser { return &Parser{} },
		DiscoverPID: DiscoverPID,
		ComputeMetrics: func(transcriptPath, sessionID string) (*session.SessionMetrics, error) {
			return ComputeMetrics(transcriptPath, sessionID)
		},
	}
}
