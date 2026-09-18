package dsh

import (
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/permission"
)

// PermissionKeyTranscripts gates all DeepSeek Harness monitoring.
const PermissionKeyTranscripts = "transcripts"

func Source() agent.Source {
	return agent.FilesUnderRoot{
		Dir:               defaultRootDir,
		DirFunc:           sessionsDir,
		SessionIDFromPath: sessionIDFromPath,
		Parser: agent.JSONLineParser{
			NewParser: func() agent.LineParser { return &Parser{} },
		},
	}
}

// Agent returns the observe-only DeepSeek Harness adapter. The process match
// uses argv because the measured process name is node. DiscoverPID uses the
// per-session session.lock writer, which the running build holds open while a
// session is live.
func Agent() agent.Agent {
	return agent.Agent{
		Identity: agent.Identity{
			Name:         AdapterName,
			DisplayName:  "DeepSeek Harness",
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
				Touches: "Reads compressed session transcripts and sibling session.lock files " +
					"under ~/.dsh/sessions/ or $DSH_HOME/sessions/",
				Detail: "Tails the highest session.vN.jsonl.zstd generation under each " +
					"$DSH_HOME/sessions/<workspace>/session-<uuid>/ directory to derive " +
					"session state, activity, model, and token usage. Checks which process " +
					"holds the sibling session.lock file open for writing to bind a live " +
					"session to its PID. Read-only — no file or DeepSeek Harness profile " +
					"is modified. Toggling off stops all reading immediately.",
			},
		},
	}
}
