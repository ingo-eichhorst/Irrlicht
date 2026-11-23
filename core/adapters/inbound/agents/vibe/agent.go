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
				// Both artifacts write one entry (#2171), so one count serves
				// both: the contract requires every variant's count to be stated
				// and no other.
				Touches: "Writes 1 hook entry to ~/.vibe/hooks.toml; for Vibe older than " +
					minVibePostAgentVersion + " also sets a flag in ~/.vibe/config.toml, and from " +
					minVibePostAgentVersion + " removes that flag. Reads the name of Vibe's " +
					"package folder under ~/.local/share/uv/tools to tell which Vibe is installed",
				Detail: hooksDetail(),
				// Apply and Writes.Verify are the legacy artifact's, the same
				// named func its variant declares. PermissionService never runs
				// them for a variant declaration — it runs the variant the
				// installed version selects (Writes.Variants, #2108) — but the
				// registry tripwires require them, the managed-writes grader
				// runs Apply directly (and grades a variant's Apply separately
				// only when it is a different func), and the legacy artifact is
				// the one that writes both declared files from an empty home.
				Apply:  applyLegacy,
				Remove: func() error { _, err := UninstallHooks(); return err },
				Writes: &agent.ManagedUserFile{
					Path:      hooksTomlPath,
					Also:      []func() (string, error){vibeConfigTomlPath},
					Uninstall: UninstallHooks,
					Verify:    VerifyHooksInstalled,
					// The envelope of the two variants below: the legacy floor
					// and no ceiling. Observed is uv's package folder name (see
					// installedVersionFromUvTool for why); the probe is the
					// fallback.
					Version: &agent.VersionGate{
						Min:      minVibeVersion,
						Probe:    []string{"vibe", "--version"},
						Observed: installedVersionFromUvTool,
					},
					Variants: hookVariants(),
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

// applyLegacy installs the legacy artifact: the permission's own Apply and
// the legacy variant's, one func so the two cannot drift.
func applyLegacy() error { _, err := EnsureHooksInstalled(); return err }

// applyCurrent installs the current artifact.
func applyCurrent() error { _, err := EnsureHooksInstalledCurrent(); return err }

// hookVariants declares the hooks permission's two artifacts (#2171): the
// legacy one up to the first Vibe that accepts post_agent, and the current one
// from there with no ceiling. The gate in Agent() is their envelope.
func hookVariants() []agent.HookVariant {
	return []agent.HookVariant{
		{
			Min:    minVibeVersion,
			Max:    minVibePostAgentVersion,
			Events: installedHookEvents,
			Since:  hookEventSince,
			Apply:  applyLegacy,
			Verify: VerifyHooksInstalled,
		},
		{
			Min:    minVibePostAgentVersion,
			Events: installedHookEventsCurrent,
			Since:  hookEventSinceCurrent,
			Apply:  applyCurrent,
			Verify: VerifyHooksInstalledCurrent,
		},
	}
}

// hooksDetail is the hooks permission's expanded consent copy. A function
// because its version sentences are rendered from the same constants the gate
// and the variants compare against (#2171), so the copy cannot drift from
// them.
func hooksDetail() string {
	return "Adds a hook entry to ~/.vibe/hooks.toml (Vibe's own hooks file). Which " +
		"entry depends on the Vibe you run, because Vibe " + minVibePostAgentVersion +
		" renamed the hook and stopped reading the flag that used to turn hooks on; " +
		"an upgrade or a downgrade rewrites the entry in place. " +
		"For Vibe " + minVibeVersion + " or newer and older than " + minVibePostAgentVersion +
		" it is a " + hookjson.EventList(installedHookEvents) + " entry, and " +
		experimentalHooksFlag + " = true is set in ~/.vibe/config.toml — the flag that " +
		"turns those versions' hook system on at all; the entry cannot fire without it. " +
		"For Vibe " + minVibePostAgentVersion + " or newer it is a " +
		hookjson.EventList(installedHookEventsCurrent) + " entry, and an " +
		experimentalHooksFlag + " line in ~/.vibe/config.toml is deleted whoever " +
		"wrote it, because those versions no longer read it and newer ones warn about " +
		"it at every start; nothing else in that file is touched. " +
		"Vibe's tool hooks (before_tool and after_tool, named pre_tool and post_tool " +
		"from " + minVibePostAgentVersion + ") are deliberately not used: the one " +
		"that runs before a tool fires before Vibe's own permission check and cannot " +
		"tell whether a call will prompt the user, so it cannot drive waiting " +
		"detection. The entry runs `irrlichd hook-post mistral-vibe` — a tiny command " +
		"irrlicht ships — which reads the daemon's own published address at the " +
		"moment the hook fires and forwards the payload; it never blocks the agent " +
		"even when the daemon is not running. " +
		"To tell which Vibe you run, irrlicht reads the name of the folder " +
		"`uv tool install` gives Vibe's package, under ~/.local/share/uv/tools/" +
		uvPackageName + " (or under $UV_TOOL_DIR or $XDG_DATA_HOME when the daemon " +
		"has them set) — only the folder's name, no file in it — and runs " +
		"`vibe --version` when that gives no answer. " +
		"On a version older than " + minVibeVersion + " nothing is written. When the " +
		"version cannot be read at all, an entry already installed and current is " +
		"kept and otherwise nothing is written, because a wrong guess writes an entry " +
		"that Vibe skips. Either way the daemon reports why. " +
		"Toggling off removes the entry (also available via " +
		"`irrlichd --uninstall-hooks`) and deletes the flag's line too, unless other " +
		"hooks remain in the file."
}
