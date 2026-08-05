package hermes

import (
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/permission"
	"irrlicht/core/domain/session"
)

// PermissionKeyStore gates all Hermes monitoring (issue #570).
const PermissionKeyStore = "store"

// Agent returns the Hermes Agent adapter declaration.
//
// Source is ProcessOwnedStore — Hermes persists no transcript files, only the
// shared SQLite store. That makes this the second adapter on that variant
// after opencode, so buildAgentWatchers dispatches on the adapter name to
// pick a store watcher (see core/cmd/irrlichd/wiring.go).
//
// Control is deliberately left at its zero value: Hermes has an interactive
// TUI that would plausibly accept injected keystrokes, but nothing in this
// PR exercised that, and declaring SupportsInput obliges the adapter to
// carry the shared control consent gate for a capability never tested.
// Wiring it is a follow-up, not an omission.
func Agent() agent.Agent {
	return agent.Agent{
		Identity: agent.Identity{
			Name:         AdapterName,
			DisplayName:  "Hermes Agent",
			IconSVGLight: iconSVGLight,
			IconSVGDark:  iconSVGDark,
		},
		Process: agent.Process{
			Match:         agent.CommandPattern{Regex: processCmdRegex},
			PIDForSession: DiscoverPID,
			ExcludeArgv:   isServiceArgv,
		},
		Source: agent.ProcessOwnedStore{
			// The store is a single shared database whose path depends on
			// $HERMES_HOME, not on the session's PID — so the pid argument
			// is ignored, the same way any constant app-data store would.
			PathForPID: func(int) string { return StorePath() },
			Reader:     metricsReader{},
		},
		Permissions: []agent.Permission{
			{
				Key:             PermissionKeyStore,
				Kind:            permission.KindObserve,
				Title:           "Read session store",
				FeatureUnlocked: "Session list, timeline, cost & token metrics",
				Touches:         "Reads $HERMES_HOME/state.db (read-only polling)",
				Detail: "Polls Hermes Agent's SQLite store at " +
					"$HERMES_HOME/state.db (default ~/.hermes/state.db) in " +
					"read-only mode to derive session state, cost, and token " +
					"metrics. Only sessions started from the CLI or TUI are " +
					"read; sessions belonging to Hermes' messaging gateway " +
					"(WhatsApp, Slack, …) share the same store and are " +
					"skipped. No row is ever written. Toggling off stops all " +
					"polling immediately.",
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
