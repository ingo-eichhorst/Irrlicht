package aider

import (
	"regexp"

	"irrlicht/core/domain/agent"
)

// commandLineRegex is the compiled form of CommandLineMatch. Eager-compiled
// at package init so the Agent() constructor and Config() share one regexp
// value (function-pointer parity is asserted in agent_parity_test.go via
// CommandLineMatch string equality, not regexp identity).
var commandLineRegex = regexp.MustCompile(commandLineMatchPattern)

const (
	commandLineMatchPattern = "/aider"
	transcriptFilename      = ".aider.chat.history.md"
)

// Aider — VT220-green block cursor on a CRT-screen circle. Mirrors aider's
// official wordmark colors (terminal green #14b014 from aider.chat/assets/logo.svg).
// Brand-consistent across light/dark appearances; the same markup serves both.
const iconSVG = `<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 100 100">
  <circle cx="50" cy="50" r="44" fill="#1f3a1f" stroke="#14b014" stroke-width="6"/>
  <rect x="40" y="32" width="20" height="36" fill="#14b014"/>
</svg>`

// Agent returns the new declaration shape introduced in #159 Phase A.
// Mirrors Config() for legacy callers and will replace Config() once the
// daemon switches over (PR2/PR3). Parity tests assert equivalence.
//
// Aider is the only currently-supported adapter using FilesUnderCWD —
// its transcript lives in each project's working directory rather than
// under a fixed root, and the format is markdown rather than JSONL.
func Agent() agent.Agent {
	p := &Parser{}
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
				ParseLineRaw: p.ParseLineRaw,
				IdleFlush:    p.IdleFlush,
			},
		},
	}
}
