package copilot

import (
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/permission"
)

// PermissionKeyTranscripts gates all GitHub Copilot monitoring (issue #570).
const PermissionKeyTranscripts = "transcripts"

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
		Source: agent.FilesUnderRoot{
			Dir:               sessionsDir(),
			SessionIDFromPath: sessionIDFromPath,
			Parser: agent.JSONLineParser{
				NewParser: func() agent.LineParser { return &Parser{} },
			},
		},
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
		},
	}
}
