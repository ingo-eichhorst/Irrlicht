package pi

import (
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
		Source: agent.FilesUnderRoot{
			Dir: sessionsDir(),
			Parser: agent.JSONLineParser{
				NewParser: func() agent.LineParser { return &Parser{} },
			},
		},
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
		},
	}
}
