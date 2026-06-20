package antigravity

import (
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/permission"
)

// PermissionKeyTranscripts gates all Antigravity monitoring (issue #570).
const PermissionKeyTranscripts = "transcripts"

// Antigravity's mark: a smooth multicolor arch (the brand's "lift" hump), not a
// literal arrow. The sweep is drawn as three solid-colored sub-arcs — blue leg,
// warm peak, green leg — meeting at shared endpoints with round caps so the
// joins blend. Solid segments (not a <linearGradient>) are deliberate: the
// macOS menu-bar renderer (NSImage(data:)) flattens an SVG gradient to a single
// flat color, so a gradient would lose the multicolor there; segments render in
// full color in both the web dashboard and the app. Dark uses Google's lighter
// tonal variants so the mark reads against dark chrome.
const iconSVGLight = `<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 100 100">
  <g fill="none" stroke-width="15" stroke-linecap="round">
    <path d="M16 82 Q27.3 39.3 38.7 25.1" stroke="#4285F4"/>
    <path d="M38.7 25.1 Q50 10.9 61.3 25.1" stroke="#EA4335"/>
    <path d="M61.3 25.1 Q72.7 39.3 84 82" stroke="#34A853"/>
  </g>
</svg>`

const iconSVGDark = `<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 100 100">
  <g fill="none" stroke-width="15" stroke-linecap="round">
    <path d="M16 82 Q27.3 39.3 38.7 25.1" stroke="#8AB4F8"/>
    <path d="M38.7 25.1 Q50 10.9 61.3 25.1" stroke="#F28B82"/>
    <path d="M61.3 25.1 Q72.7 39.3 84 82" stroke="#81C995"/>
  </g>
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
