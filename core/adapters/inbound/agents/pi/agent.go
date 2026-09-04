package pi

import (
	"irrlicht/core/adapters/inbound/agents/hookjson"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/permission"
)

// PermissionKeyTranscripts gates all Pi monitoring (issue #570).
const PermissionKeyTranscripts = "transcripts"

// Pi coding agent — Greek letter pi in a circle. Color picks contrast against
// the surrounding chrome: near-black on light themes, near-white on dark.
const iconSVGLight = `<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 100 100">
  <circle cx="50" cy="50" r="44" fill="none" stroke="#1A1A1A" stroke-width="8"/>
  <line x1="28" y1="30" x2="72" y2="30" stroke="#1A1A1A" stroke-width="8" stroke-linecap="round"/>
  <line x1="40" y1="30" x2="40" y2="74" stroke="#1A1A1A" stroke-width="8" stroke-linecap="round"/>
  <line x1="60" y1="30" x2="64" y2="74" stroke="#1A1A1A" stroke-width="8" stroke-linecap="round"/>
</svg>`

const iconSVGDark = `<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 100 100">
  <circle cx="50" cy="50" r="44" fill="none" stroke="#E0E0E0" stroke-width="8"/>
  <line x1="28" y1="30" x2="72" y2="30" stroke="#E0E0E0" stroke-width="8" stroke-linecap="round"/>
  <line x1="40" y1="30" x2="40" y2="74" stroke="#E0E0E0" stroke-width="8" stroke-linecap="round"/>
  <line x1="60" y1="30" x2="64" y2="74" stroke="#E0E0E0" stroke-width="8" stroke-linecap="round"/>
</svg>`

// Source declares where Pi's transcripts live. A function, not a value, so
// the hook receiver's confiner (hooks.go's transcriptConfiner) and the
// daemon's fswatcher resolve the SAME root from the SAME declaration — a
// second, constants-derived list is exactly the drift issue #1361 was about,
// and sessionsDir() reads environment overrides that a package-level value
// would freeze at init time.
func Source() agent.Source {
	return agent.FilesUnderRoot{
		Dir: sessionsDir(),
		Parser: agent.JSONLineParser{
			NewParser: func() agent.LineParser { return &Parser{} },
		},
	}
}

// Agent returns the Pi adapter registration.
func Agent() agent.Agent {
	return agent.Agent{
		Identity: agent.Identity{
			Name:         AdapterName,
			DisplayName:  "Pi",
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
				FeatureUnlocked: "Session list, timeline, cost & token metrics",
				Touches:         "Reads session transcripts under ~/.pi/agent/sessions/",
				Detail: "Tails *.jsonl session files under ~/.pi/agent/sessions/ to " +
					"derive session state, cost, and token metrics. Read-only — no " +
					"file is ever modified. Toggling off stops all reading " +
					"immediately.",
			},
			{
				Key:             PermissionKeyHooks,
				Kind:            permission.KindModify,
				Title:           "Install status hooks",
				FeatureUnlocked: "Authoritative turn-end detection",
				// "1 hook entry" is what contracttesting's #1356 count arm
				// reads, and it is the honest number: one subscription, to
				// one event. What is unusual is the SHAPE that subscription
				// takes, which is why Touches names it in the same breath
				// rather than leaving it to Detail — the wizard row is what
				// most users read.
				Touches: "Writes 1 hook entry to " + displayExtensionPath +
					" — a small JavaScript file that pi loads and runs",
				Detail: "pi has no hooks section in its settings: the only way to observe " +
					"its lifecycle is an extension, so this permission installs one. That " +
					"is a single JavaScript file at " + displayExtensionPath + ", which pi " +
					"auto-discovers and executes inside every pi session with your own " +
					"permissions. It is code irrlicht ships in its own binary, not a " +
					"package fetched from anywhere — no npm, no git, no `pi install` — and " +
					"pi's own settings.json is not touched at all. The file imports only " +
					"node:child_process, subscribes to exactly one pi event (agent_settled, " +
					"which fires when pi will not continue running on its own) and does one " +
					"thing with it: runs `irrlichd hook-post pi`, a tiny command irrlicht " +
					"ships, handing it the session's transcript path. That command reads the " +
					"daemon's own published address at the moment the hook fires, so nothing " +
					"in the file names a host or a port and it cannot go stale; it also never " +
					"blocks pi, even when the daemon is not running. The file deliberately " +
					"does not subscribe to pi's blocking tool_call event, so it cannot " +
					"interfere with a tool call. " +
					hookjson.RequiresVersion("pi", minPiVersion) +
					" Toggling off deletes the file (also available via " +
					"`irrlichd --uninstall-hooks`); nothing else in your pi installation was " +
					"changed, so there is nothing else to undo.",
				Apply:  func() error { _, err := EnsureHooksInstalled(); return err },
				Remove: func() error { _, err := UninstallHooks(); return err },
				Writes: &agent.ManagedUserFile{
					Path:      ExtensionPath,
					Uninstall: UninstallHooks,
					Verify:    VerifyHooksInstalled,
					Version: &agent.VersionGate{
						Min:   minPiVersion,
						Probe: []string{"pi", "--version"},
					},
				},
			},
		},
	}
}
