package vibe

import (
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/backchannel"
	"irrlicht/core/domain/permission"
)

// PermissionKeyTranscripts gates all Mistral Vibe monitoring (issue #570).
const PermissionKeyTranscripts = "transcripts"

// Agent returns the Mistral Vibe adapter registration. The Source watches
// ~/.vibe/logs/session and derives each session's ID from the <session-id>
// directory (sessionIDFromPath) since the transcript filename is the constant
// messages.jsonl. The CommandPattern matcher binds the Python `vibe` process
// for liveness (an ExactName match on "vibe" would never fire — the OS process
// name is the interpreter). Control is declared because Vibe runs an interactive
// Textual TUI/REPL that reads terminal input, so the daemon can forward replies,
// interrupts (Ctrl-C), and the /compact preset through its terminal backend —
// gated behind the ControlPermission and the backchannel beta toggle.
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
			DirFunc:           sessionsDir,
			SessionIDFromPath: sessionIDFromPath,
			Parser: agent.JSONLineParser{
				NewParser: func() agent.LineParser { return &Parser{} },
			},
		},
		Control: agent.Control{
			SupportsInput: true,
			Interrupt:     agent.InterruptCtrlC,
			Presets: map[string]string{
				backchannel.PresetCompact: "/compact",
			},
		},
		Permissions: []agent.Permission{
			agent.ControlPermission(),
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
		},
	}
}
