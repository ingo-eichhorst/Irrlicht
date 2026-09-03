package vibe

import (
	"irrlicht/core/adapters/inbound/agents/hookjson"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/permission"
)

// PermissionKeyTranscripts gates all Mistral Vibe monitoring (issue #570).
const PermissionKeyTranscripts = "transcripts"

// Source is the adapter's transcript-tree declaration, shared by Agent() and
// the hook receiver's path confiner (issue #1718, hooks.go) so the tree the
// daemon watches and the tree the receiver confines caller-supplied
// transcript paths to cannot drift apart — the same reasoning geminicli's
// Source() documents.
func Source() agent.Source {
	return agent.FilesUnderRoot{
		DirFunc:           sessionsDir,
		SessionIDFromPath: sessionIDFromPath,
		Parser: agent.JSONLineParser{
			NewParser: func() agent.LineParser { return &Parser{} },
		},
	}
}

// Agent returns the Mistral Vibe adapter registration. The Source watches
// ~/.vibe/logs/session and derives each session's ID from the <session-id>
// directory (sessionIDFromPath) since the transcript filename is the constant
// messages.jsonl. The CommandPattern matcher binds the Python `vibe` process
// for liveness (an ExactName match on "vibe" would never fire — the OS process
// name is the interpreter).
func Agent() agent.Agent {
	return agent.Agent{
		Identity: agent.Identity{
			Name:         AdapterName,
			DisplayName:  "Mistral Vibe",
			IconSVGLight: iconSVGLight,
			IconSVGDark:  iconSVGDark,
		},
		Process: agent.Process{
			Match:         agent.CommandPattern{Regex: processCmdRegex},
			PIDForSession: DiscoverPID,
		},
		Source: Source(),
		Permissions: []agent.Permission{
			{
				Key:             PermissionKeyTranscripts,
				Kind:            permission.KindObserve,
				Title:           "Read session transcripts",
				FeatureUnlocked: "Session list, timeline, state, model & context-window usage",
				Touches: "Reads session transcripts and their meta.json sidecar under " +
					"~/.vibe/logs/session/, ~/.vibe/config.toml to locate that folder, and " +
					"the working directory of running vibe processes",
				Detail: "Tails messages.jsonl files under ~/.vibe/logs/session/<session-id>/ " +
					"to derive session state, activity, and timeline, and reads the sibling " +
					"meta.json sidecar for the working directory, active model, and context-" +
					"token count (context-window usage). Reads one key from ~/.vibe/config.toml " +
					"([session_logging].save_dir) because it can move that folder elsewhere — " +
					"no other setting is read. Also scans for running vibe processes " +
					"and reads their working directory to bind a session to its process. " +
					"Read-only — no file is ever modified. Toggling off stops all reading " +
					"immediately.",
			},
			{
				Key:             PermissionKeyHooks,
				Kind:            permission.KindModify,
				Title:           "Install status hooks",
				FeatureUnlocked: "Authoritative turn-end detection",
				// Deliberately NOT "N hook entries" using hookjson.EntriesTouched:
				// that helper hardcodes the plural, and the #1356 contract's own
				// count regex accepts singular specifically so a one-event
				// adapter is not forced into bad grammar — this is that adapter.
				Touches: "Writes 1 hook entry to ~/.vibe/hooks.toml, and a flag in ~/.vibe/config.toml",
				Detail: "Adds a post_agent_turn hook entry to ~/.vibe/hooks.toml (Vibe's own " +
					"hooks file) and sets enable_experimental_hooks = true in " +
					"~/.vibe/config.toml — the flag that turns Vibe's hook system on at all; " +
					"the entry cannot fire without it. Vibe's before_tool/after_tool events are " +
					"deliberately not used: before_tool fires before Vibe's own permission " +
					"check runs and cannot tell whether a call will prompt the user, so it " +
					"cannot drive waiting detection. The entry runs `irrlichd hook-post " +
					"mistral-vibe` — a tiny command irrlicht ships — which reads the daemon's " +
					"own published address at the moment the hook fires and forwards the " +
					"payload; it never blocks the agent even when the daemon is not running. " +
					hookjson.RequiresVersion("Mistral Vibe", minVibeVersion) +
					" Toggling off removes the entry (also available via " +
					"`irrlichd --uninstall-hooks`) and clears the flag too, unless other " +
					"hooks remain in the file.",
				Apply:  func() error { _, err := EnsureHooksInstalled(); return err },
				Remove: func() error { _, err := UninstallHooks(); return err },
				Writes: &agent.ManagedUserFile{
					Path:      hooksTomlPath,
					Also:      []func() (string, error){vibeConfigTomlPath},
					Uninstall: UninstallHooks,
					Verify:    VerifyHooksInstalled,
					Version: &agent.VersionGate{
						Min:   minVibeVersion,
						Probe: []string{"vibe", "--version"},
					},
					// #1753's real fixture (captured before #1756 existed to
					// require declaring it) — see hookinstaller_realconfig_test.go.
					RealFixture: &agent.RealConfigFixture{
						Path:       "testdata/real-config-2.19.1.toml",
						CLIVersion: "2.19.1",
					},
				},
			},
		},
	}
}
