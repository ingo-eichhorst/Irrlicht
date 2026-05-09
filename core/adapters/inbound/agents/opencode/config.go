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
// OpenCode — curly braces { } in a circle, rendered in OpenCode's brand blue.
// Background swaps for theme so the icon doesn't blow out against light or
// dark surrounding chrome; foreground stays brand blue (#3B82F6) in both.
const iconSVGLight = `<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 100 100">
  <circle cx="50" cy="50" r="44" fill="#F0F6FF" stroke="#3B82F6" stroke-width="6"/>
  <path d="M42 28 Q30 28 30 38 L30 46 Q30 50 26 50 Q30 50 30 54 L30 62 Q30 72 42 72" fill="none" stroke="#3B82F6" stroke-width="7" stroke-linecap="round" stroke-linejoin="round"/>
  <path d="M58 28 Q70 28 70 38 L70 46 Q70 50 74 50 Q70 50 70 54 L70 62 Q70 72 58 72" fill="none" stroke="#3B82F6" stroke-width="7" stroke-linecap="round" stroke-linejoin="round"/>
</svg>`

const iconSVGDark = `<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 100 100">
  <circle cx="50" cy="50" r="44" fill="#0D1117" stroke="#3B82F6" stroke-width="6"/>
  <path d="M42 28 Q30 28 30 38 L30 46 Q30 50 26 50 Q30 50 30 54 L30 62 Q30 72 42 72" fill="none" stroke="#3B82F6" stroke-width="7" stroke-linecap="round" stroke-linejoin="round"/>
  <path d="M58 28 Q70 28 70 38 L70 46 Q70 50 74 50 Q70 50 70 54 L70 62 Q70 72 58 72" fill="none" stroke="#3B82F6" stroke-width="7" stroke-linecap="round" stroke-linejoin="round"/>
</svg>`

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
		DisplayName:  "OpenCode",
		IconSVGLight: iconSVGLight,
		IconSVGDark:  iconSVGDark,
	}
}
