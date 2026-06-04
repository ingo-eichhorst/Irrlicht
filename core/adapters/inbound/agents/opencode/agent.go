package opencode

import (
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/permission"
	"irrlicht/core/domain/session"
)

// PermissionKeyDatabase gates all OpenCode monitoring (issue #570).
const PermissionKeyDatabase = "database"

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

// OpenCode is the only currently-supported adapter using ProcessOwnedStore
// — session state lives in a SQLite database rather than JSONL files.
// The daemon's wiring today drives metric computation through Reader using
// the agent.Event.TranscriptPath supplied by the dedicated opencode
// watcher (constructed inline in cmd/irrlichd/main.go); PathForPID is not
// yet called.
func Agent() agent.Agent {
	return agent.Agent{
		Identity: agent.Identity{
			Name:         AdapterName,
			DisplayName:  "OpenCode",
			IconSVGLight: iconSVGLight,
			IconSVGDark:  iconSVGDark,
		},
		Process: agent.Process{
			Match:         agent.ExactName{Name: ProcessName},
			PIDForSession: DiscoverPID,
		},
		Source: agent.ProcessOwnedStore{
			// Panic rather than return "" so a future caller that wires
			// this in fails loudly; the silent-empty path would have given
			// ComputeMetrics an empty filename and produced no rows.
			PathForPID: func(int) string {
				panic("opencode.Agent().Source.PathForPID: resolver not installed")
			},
			Reader: metricsReader{},
		},
		Permissions: []agent.Permission{
			{
				Key:             PermissionKeyDatabase,
				Kind:            permission.KindObserve,
				Title:           "Read session database",
				FeatureUnlocked: "Session list, timeline, cost & token metrics",
				Touches:         "Reads ~/.local/share/opencode/opencode.db (read-only polling)",
				Detail: "Polls OpenCode's SQLite database at " +
					"~/.local/share/opencode/opencode.db in read-only mode to derive " +
					"session state, cost, and token metrics. No row is ever written. " +
					"Toggling off stops all polling immediately.",
			},
		},
	}
}

// metricsReader adapts the package-level ComputeMetrics function to the
// agent.MetricsReader interface.
type metricsReader struct{}

func (metricsReader) ComputeMetrics(storePath, sessionID string) (*session.SessionMetrics, error) {
	return ComputeMetrics(storePath, sessionID)
}
