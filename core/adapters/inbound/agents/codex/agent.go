package codex

import (
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/permission"
)

// PermissionKeyTranscripts gates all Codex monitoring (issue #570).
const PermissionKeyTranscripts = "transcripts"

// Codex — circle with >_ terminal prompt. Color picks contrast against
// the surrounding chrome: near-black on light themes, near-white on dark.
const iconSVGLight = `<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 100 100">
  <circle cx="50" cy="50" r="44" fill="none" stroke="#1A1A1A" stroke-width="8"/>
  <path d="M28 38 L42 50 L28 62" fill="none" stroke="#1A1A1A" stroke-width="7" stroke-linecap="round" stroke-linejoin="round"/>
  <line x1="48" y1="62" x2="68" y2="62" stroke="#1A1A1A" stroke-width="7" stroke-linecap="round"/>
</svg>`

const iconSVGDark = `<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 100 100">
  <circle cx="50" cy="50" r="44" fill="none" stroke="#E0E0E0" stroke-width="8"/>
  <path d="M28 38 L42 50 L28 62" fill="none" stroke="#E0E0E0" stroke-width="7" stroke-linecap="round" stroke-linejoin="round"/>
  <line x1="48" y1="62" x2="68" y2="62" stroke="#E0E0E0" stroke-width="7" stroke-linecap="round"/>
</svg>`

// Agent returns the Codex adapter registration.
func Agent() agent.Agent {
	return agent.Agent{
		Identity: agent.Identity{
			Name:         AdapterName,
			DisplayName:  "Codex",
			IconSVGLight: iconSVGLight,
			IconSVGDark:  iconSVGDark,
		},
		Process: agent.Process{
			Match:         agent.ExactName{Name: ProcessName},
			PIDForSession: DiscoverPID,
		},
		Source: agent.FilesUnderRoot{
			Dir: sessionsDir(),
			Parser: agent.JSONLineParser{
				NewParser: func() agent.LineParser { return &Parser{} },
			},
		},
		Control: agent.Control{SupportsInput: true, Interrupt: agent.InterruptCtrlC},
		Permissions: []agent.Permission{
			agent.ControlPermission(),
			{
				Key:             PermissionKeyTranscripts,
				Kind:            permission.KindObserve,
				Title:           "Read session transcripts",
				FeatureUnlocked: "Session list, timeline, cost & token metrics",
				Touches:         "Reads session transcripts under ~/.codex/sessions/",
				Detail: "Tails *.jsonl session files under ~/.codex/sessions/YYYY/MM/DD/ " +
					"to derive session state, cost, and token metrics. Read-only — " +
					"no file is ever modified. Toggling off stops all reading " +
					"immediately.",
			},
		},
	}
}
