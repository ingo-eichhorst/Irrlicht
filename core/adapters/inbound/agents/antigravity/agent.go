package antigravity

import (
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/permission"
)

// PermissionKeyTranscripts gates all Antigravity monitoring (issue #570).
const PermissionKeyTranscripts = "transcripts"

// Antigravity — an upward arrow (anti-gravity / lift). Google blue on light,
// the lighter dark-mode blue on dark so the mark reads against either chrome.
const iconSVGLight = `<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 100 100">
  <path d="M50 12 L82 56 H62 V84 H38 V56 H18 Z" fill="#4285F4"/>
</svg>`

const iconSVGDark = `<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 100 100">
  <path d="M50 12 L82 56 H62 V84 H38 V56 H18 Z" fill="#8AB4F8"/>
</svg>`

// Agent returns the Antigravity adapter registration. One adapter covers both
// the `agy` CLI and the Antigravity IDE: the Source watches both brain stores
// (CLI via Dir, IDE via ExtraDirs) and derives each session's ID from the
// <conv-id> directory (SessionIDFromPath) since the transcript filename is
// constant. The ExactName matcher binds CLI processes for liveness; IDE
// sessions have no per-conversation process and stay transcript-only.
func Agent() agent.Agent {
	return agent.Agent{
		Identity: agent.Identity{
			Name:         AdapterName,
			DisplayName:  "Antigravity",
			IconSVGLight: iconSVGLight,
			IconSVGDark:  iconSVGDark,
		},
		Process: agent.Process{
			Match:         agent.ExactName{Name: ProcessName},
			PIDForSession: DiscoverPID,
		},
		Source: agent.FilesUnderRoot{
			Dir:               cliBrainDir,
			ExtraDirs:         []string{ideBrainDir},
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
				FeatureUnlocked: "Session list, timeline, and state",
				Touches: "Reads session transcripts under ~/.gemini/antigravity-cli/ " +
					"and ~/.gemini/antigravity/ and the working directory of running " +
					"agy processes",
				Detail: "Tails transcript.jsonl files under " +
					"~/.gemini/antigravity{,-cli}/brain/<conversation>/.system_generated/logs/ " +
					"to derive session state, model, and timeline. Also scans for " +
					"running agy CLI processes and reads their working directory to " +
					"bind a session to its process. Read-only — no file is ever " +
					"modified. Toggling off stops all reading immediately.",
			},
		},
	}
}
