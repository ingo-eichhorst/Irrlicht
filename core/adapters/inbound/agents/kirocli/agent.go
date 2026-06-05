package kirocli

import (
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/permission"
)

// PermissionKeyTranscripts gates all Kiro CLI monitoring (issue #570).
const PermissionKeyTranscripts = "transcripts"

// Kiro CLI — the Kiro ghost mark. Color picks contrast against the
// surrounding chrome: near-black on light themes, near-white on dark.
const iconSVGLight = `<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 100 100">
  <path d="M50 10 C28 10 16 27 16 48 V88 L28 77 L39 88 L50 77 L61 88 L72 77 L84 88 V48 C84 27 72 10 50 10 Z" fill="none" stroke="#1A1A1A" stroke-width="8" stroke-linejoin="round"/>
  <circle cx="38" cy="46" r="6" fill="#1A1A1A"/>
  <circle cx="62" cy="46" r="6" fill="#1A1A1A"/>
</svg>`

const iconSVGDark = `<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 100 100">
  <path d="M50 10 C28 10 16 27 16 48 V88 L28 77 L39 88 L50 77 L61 88 L72 77 L84 88 V48 C84 27 72 10 50 10 Z" fill="none" stroke="#E0E0E0" stroke-width="8" stroke-linejoin="round"/>
  <circle cx="38" cy="46" r="6" fill="#E0E0E0"/>
  <circle cx="62" cy="46" r="6" fill="#E0E0E0"/>
</svg>`

// Agent returns the Kiro CLI adapter registration.
func Agent() agent.Agent {
	return agent.Agent{
		Identity: agent.Identity{
			Name:         AdapterName,
			DisplayName:  "Kiro CLI",
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
				FeatureUnlocked: "Session list, timeline & live state",
				Touches:         "Reads session transcripts under ~/.kiro/sessions/cli/",
				Detail: "Tails *.jsonl session files under ~/.kiro/sessions/cli/ to " +
					"derive session state and activity, and reads the *.json " +
					"metadata sidecar for the working directory. Read-only — no " +
					"file is ever modified. Toggling off stops all reading " +
					"immediately.",
			},
		},
	}
}
