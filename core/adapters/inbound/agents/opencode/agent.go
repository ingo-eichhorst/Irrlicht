package opencode

import (
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/session"
)

// Agent returns the new declaration shape introduced in #159 Phase A.
// Mirrors Config() for legacy callers and will replace Config() once the
// daemon switches over (PR2/PR3). Parity tests assert equivalence.
//
// OpenCode is the only currently-supported adapter using ProcessOwnedStore
// — session state lives in a SQLite database rather than JSONL files.
// PathForPID is a placeholder until PR5 wires the runtime dispatch; the
// legacy shim in PR2 still drives metric computation through the Reader
// directly using the adapter-supplied path string.
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
			// Placeholder — runtime dispatch (PR5) will replace with the
			// real PID→DB-path resolver. The legacy shim in PR2 does not
			// call PathForPID; it uses the agent.Event.TranscriptPath
			// supplied by the existing opencode watcher.
			PathForPID: func(int) string { return "" },
			Reader:     metricsReader{},
		},
	}
}

// metricsReader implements agent.MetricsReader by delegating to the
// existing package-level ComputeMetrics function. Identity of behavior is
// trivially preserved.
type metricsReader struct{}

func (metricsReader) ComputeMetrics(storePath, sessionID string) (*session.SessionMetrics, error) {
	return ComputeMetrics(storePath, sessionID)
}
