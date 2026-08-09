package copilot

import (
	"irrlicht/core/adapters/inbound/agents/hookjson"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/permission"
)

// PermissionKeyTranscripts gates all GitHub Copilot monitoring (issue #570).
const PermissionKeyTranscripts = "transcripts"

// displayName is the user-facing CLI name used in consent copy.
const displayName = "GitHub Copilot CLI"

// Source is the adapter's transcript-tree declaration.
//
// The hook receiver's path confiner reads it per request (issue #1361), and
// Agent() below is its only other caller — one declaration, so the tree the
// daemon watches and the tree the receiver confines to cannot drift apart.
func Source() agent.Source {
	return agent.FilesUnderRoot{
		Dir:               sessionsDir(),
		SessionIDFromPath: sessionIDFromPath,
		Parser: agent.JSONLineParser{
			NewParser: func() agent.LineParser { return &Parser{} },
		},
	}
}

// Agent returns the GitHub Copilot adapter registration. The Source watches
// ~/.copilot/session-state and derives each session's ID from the <session-id>
// directory (sessionIDFromPath) since the transcript filename is the constant
// events.jsonl. The ExactName matcher binds the native `copilot` binary for
// liveness — the npm distribution spawns it from a node loader, and pgrep -x
// matches the child's accounting name rather than the parent's.
//
// Control is deliberately NOT declared yet. Copilot runs an interactive TUI
// that reads terminal input, so backchannel forwarding is plausibly feasible
// (the same shape as mistral-vibe and kiro-cli), but nothing here has been
// verified live against a driven session — which slash-command presets exist,
// and whether Ctrl-C interrupts a turn without killing the REPL. Declaring
// SupportsInput on an unverified assumption would advertise a capability the
// daemon might not be able to deliver; the onboarding factory's backchannel
// scenarios are the mechanism that settles it with evidence.
func Agent() agent.Agent {
	return agent.Agent{
		Identity: agent.Identity{
			Name:         AdapterName,
			DisplayName:  "GitHub Copilot",
			IconSVGLight: iconSVGLight,
			IconSVGDark:  iconSVGDark,
		},
		Process: agent.Process{
			Match:         agent.ExactName{Name: ProcessName},
			PIDForSession: DiscoverPID,
		},
		Source: Source(),
		Permissions: []agent.Permission{
			{
				Key:             PermissionKeyTranscripts,
				Kind:            permission.KindObserve,
				Title:           "Read session transcripts",
				FeatureUnlocked: "Session list, timeline, state, cost & token metrics",
				Touches: "Reads session transcripts under ~/.copilot/session-state/ and " +
					"the working directory of running copilot processes",
				Detail: "Tails events.jsonl files under ~/.copilot/session-state/<session-id>/ " +
					"to derive session state, activity, and timeline, and reads the token and " +
					"AI-credit counters GitHub Copilot records there for cost metrics. Sibling " +
					"files in the same folder (workspace.yaml, session.db, checkpoints) are not " +
					"read. Also scans for running copilot processes and reads their working " +
					"directory to bind a session to its process. Read-only — no file is ever " +
					"modified. Toggling off stops all reading immediately.",
			},
			{
				Key:             PermissionKeyHooks,
				Kind:            permission.KindModify,
				Title:           "Install status hooks",
				FeatureUnlocked: "Authoritative turn-end detection, and an instant permission-prompt refresh",
				// Count and event list are derived from installedHookEvents,
				// never restated: this copy is the consent contract, and
				// hand-maintaining it is how claudecode's came to promise six
				// entries against a seven-event install (#1356).
				Touches: hookjson.EntriesTouched("~/.copilot/hooks/irrlicht.json", installedHookEvents),
				Detail: "Adds " + hookjson.EventList(installedHookEvents) +
					" hook entries that POST the hook payload to the local daemon at " +
					hookEndpointURL() + " via GitHub Copilot's native http hook " +
					"(no shell, no curl). The file is irrlicht's own — Copilot unions every " +
					"*.json in that directory, so nothing you wrote there is read or rewritten. " +
					hookjson.RequiresVersion(displayName, minCLIVersion) +
					" IMPORTANT: Copilot refuses to deliver http hooks to a loopback address " +
					"unless " + RequiresLocalhostOptIn + "=1 is set in the environment Copilot " +
					"itself runs in. irrlicht cannot set that for you, and until you export it " +
					"these entries are written but never fire. " +
					"Toggling off removes exactly these entries (also available via " +
					"`irrlichd --uninstall-hooks`).",
				Apply:  func() error { _, err := EnsureHooksInstalled(); return err },
				Remove: func() error { _, err := UninstallHooks(); return err },
				Hooks: &agent.HookInstall{
					ConfigPath: copilotHooksPath,
					Uninstall:  UninstallHooks,
					Verify:     VerifyHooksInstalled,
					Version: &agent.VersionGate{
						Min:      minCLIVersion,
						Probe:    []string{"copilot", "--version"},
						Observed: newestObservedCLIVersion,
					},
				},
			},
		},
	}
}
