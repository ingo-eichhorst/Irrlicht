package aider

import (
	"regexp"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/permission"
)

// PermissionKeyHistory gates all Aider monitoring (issue #570).
const PermissionKeyHistory = "history"

// Aider's actual OS process is `python` invoking the aider script (uv/pipx
// wrapper), so `pgrep -x aider` finds nothing. The leading slash anchors
// to the binary path and excludes wrappers (tmux, sh) that mention `aider`
// in their own argv.
var commandLineRegex = regexp.MustCompile("/aider")

// Aider writes its chat history per-project (in CWD), not under ~/.aider.
const transcriptFilename = ".aider.chat.history.md"

// VT220-green block cursor on a CRT-screen circle. Brand colors from
// aider.chat/assets/logo.svg; light/dark themes share one markup.
const iconSVG = `<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 100 100">
  <circle cx="50" cy="50" r="44" fill="#1f3a1f" stroke="#14b014" stroke-width="6"/>
  <rect x="40" y="32" width="20" height="36" fill="#14b014"/>
</svg>`

// Aider is the only currently-supported adapter using FilesUnderCWD — its
// transcript lives in each project's working directory rather than under
// a fixed root, and the format is markdown rather than JSONL.
//
// RawLineParser carries a NewParser factory rather than bound method
// values so each running aider process gets its own Parser instance;
// Aider's Parser tracks idle-flush state that must not collide across
// concurrent processes targeting different project CWDs.
func Agent() agent.Agent {
	return agent.Agent{
		Identity: agent.Identity{
			Name:         AdapterName,
			DisplayName:  "Aider",
			IconSVGLight: iconSVG,
			IconSVGDark:  iconSVG,
		},
		Process: agent.Process{
			Match:         agent.CommandPattern{Regex: commandLineRegex},
			PIDForSession: DiscoverPID,
		},
		Source: agent.FilesUnderCWD{
			Filename: transcriptFilename,
			Parser: agent.RawLineParser{
				NewParser: func() agent.RawParser { return &Parser{} },
			},
		},
		Control: agent.Control{SupportsInput: true, Interrupt: agent.InterruptCtrlC},
		Permissions: []agent.Permission{
			agent.ControlPermission(),
			{
				Key:             PermissionKeyHistory,
				Kind:            permission.KindObserve,
				Title:           "Read chat history",
				FeatureUnlocked: "Session list, timeline, cost & token metrics",
				Touches:         "Reads " + transcriptFilename + " in each project directory",
				Detail: "Tails the " + transcriptFilename + " file Aider writes in " +
					"the working directory of each running aider process. Read-only " +
					"— no file is ever modified. Toggling off stops all reading " +
					"immediately.",
			},
		},
	}
}
