package junie

import (
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/permission"
)

// PermissionKeyTranscripts gates all Junie monitoring.
const PermissionKeyTranscripts = "transcripts"

// Source is the adapter's transcript-tree declaration. Junie's session root
// is a plain $HOME-relative constant (no env var or config file relocates it
// — see defaultRootDir), so the static Dir field suffices; no DirFunc is
// needed. The session ID comes from the <session-id> directory
// (sessionIDFromPath) since the transcript filename is the constant
// events.jsonl, and that function also skips the root's index.jsonl, the
// sibling state.json/transcript.md, and task-* subdirectory files.
func Source() agent.Source {
	return agent.FilesUnderRoot{
		Dir:               defaultRootDir,
		SessionIDFromPath: sessionIDFromPath,
		Parser: agent.JSONLineParser{
			NewParser: func() agent.LineParser { return &Parser{} },
		},
	}
}

// Agent returns the JetBrains Junie adapter registration. The CommandPattern
// matcher binds junie processes on their command line (the binary path ends
// in .../junie, possibly inside junie.app/Contents/MacOS/), and DiscoverPID
// prefers Junie's own ~/.junie/processes/ sidecars over the CWD scan (pid.go).
//
// Junie exposes no hook system, so the adapter is observe-only (the aider
// pattern): a single transcripts permission and no hooks — turn-end
// detection comes from the TaskState events Junie writes explicitly, so no
// idle-flush heuristic or hook is needed.
func Agent() agent.Agent {
	return agent.Agent{
		Identity: agent.Identity{
			Name:         AdapterName,
			DisplayName:  "Junie",
			IconSVGLight: iconSVGLight,
			IconSVGDark:  iconSVGDark,
		},
		Process: agent.Process{
			Match:         agent.CommandPattern{Regex: processCmdRegex},
			PIDForSession: DiscoverPID,
		},
		Source: Source(),
		Permissions: []agent.Permission{
			{
				Key:             PermissionKeyTranscripts,
				Kind:            permission.KindObserve,
				Title:           "Read session transcripts",
				FeatureUnlocked: "Session list, timeline, state, model & context-window usage",
				Touches: "Reads session transcripts under ~/.junie/sessions/, process " +
					"sidecar files under ~/.junie/processes/, and the working directory " +
					"of running junie processes",
				Detail: "Tails events.jsonl files under ~/.junie/sessions/<session-id>/ " +
					"to derive session state, activity, and timeline. Reads the small " +
					"JSON sidecars Junie writes under ~/.junie/processes/ to bind a " +
					"session to its process ID and project directory. Also scans for " +
					"running junie processes and reads their working directory when no " +
					"sidecar answers. Read-only — no file is ever modified. Toggling " +
					"off stops all reading immediately.",
			},
		},
	}
}
