package vibe

import (
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/permission"
)

// PermissionKeyTranscripts gates all Mistral Vibe monitoring (issue #570).
const PermissionKeyTranscripts = "transcripts"

// Agent returns the Mistral Vibe adapter registration. The Source watches
// ~/.vibe/logs/session and derives each session's ID from the <session-id>
// directory (sessionIDFromPath) since the transcript filename is the constant
// messages.jsonl. The CommandPattern matcher binds the Python `vibe` process
// for liveness (an ExactName match on "vibe" would never fire — the OS process
// name is the interpreter). No Control is declared: Vibe's backchannel
// (interactive input injection) is unverified, so the adapter is read-only
// until a live drive confirms it.
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
				FeatureUnlocked: "Session list, timeline, state, model & context-window usage",
				Touches: "Reads session transcripts and their meta.json sidecar under " +
					"~/.vibe/logs/session/ and the working directory of running vibe processes",
				Detail: "Tails messages.jsonl files under ~/.vibe/logs/session/<session-id>/ " +
					"to derive session state, activity, and timeline, and reads the sibling " +
					"meta.json sidecar for the working directory, active model, and context-" +
					"token count (context-window usage). Also scans for running vibe processes " +
					"and reads their working directory to bind a session to its process. " +
					"Read-only — no file is ever modified. Toggling off stops all reading " +
					"immediately.",
			},
		},
	}
}
