package hermes

import (
	"irrlicht/core/adapters/inbound/agents/hookjson"
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
			{
				Key:             PermissionKeyHooks,
				Kind:            permission.KindModify,
				Title:           "Install status hooks",
				FeatureUnlocked: "Blocked-on-user detection and authoritative turn-end detection",
				// "3 hook entries" is what contracttesting's #1356 count arm
				// reads. The second file is named in the same breath rather
				// than left to Detail, because an install that wrote only the
				// config would never fire and the wizard row is what most
				// users read.
				Touches: "Writes 3 hook entries to " + displayConfigPath +
					" and the matching approvals to " + displayAllowlistPath,
				Detail: "Hermes Agent runs a shell command on lifecycle events, declared as a " +
					"`hooks:` block in " + displayConfigPath + ". This adds one entry for each " +
					"of three events (" + hookjson.EventList(installedHookEvents) + ") inside a " +
					"clearly marked block of its own; every other line of your config, comments " +
					"included, is left byte-for-byte alone, and the install refuses rather than " +
					"guessing if the file is shaped in a way it cannot edit safely. No code is " +
					"installed anywhere: each entry runs `irrlichd hook-post hermes`, a tiny " +
					"command irrlicht ships, which reads the daemon's own published address at " +
					"the moment the event fires — so nothing names a host or a port, nothing can " +
					"go stale, and it never blocks Hermes even when the daemon is not running. " +
					"Hermes will not run a configured hook until you approve it, so this also " +
					"records that approval in " + displayAllowlistPath + " — the same record " +
					"Hermes' own prompt would write, which granting here is you answering. The " +
					"three events are observers: none of them is one whose output Hermes reads " +
					"as a decision, so this can never veto a command or keep a turn running. " +
					hookjson.RequiresVersion("hermes", minHermesVersion) +
					" Toggling off removes the block and the approvals (also available via " +
					"`irrlichd --uninstall-hooks`); nothing else in your Hermes installation " +
					"was changed.",
				Apply:  func() error { _, err := EnsureHooksInstalled(); return err },
				Remove: func() error { _, err := UninstallHooks(); return err },
				Writes: &agent.ManagedUserFile{
					Path:      ConfigPath,
					Uninstall: UninstallHooks,
					Verify:    VerifyHooksInstalled,
					// The allowlist is the other half of the install: without
					// the approval Hermes skips registration and logs
					// "not allowlisted", so a declaration naming only the
					// config would leave a file the recorder does not back up
					// and #1449's refusal does not judge.
					Also: []func() (string, error){AllowlistPath},
					Version: &agent.VersionGate{
						Min:   minHermesVersion,
						Probe: []string{"hermes", "--version"},
					},
				},
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
