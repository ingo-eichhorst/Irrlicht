package muse

import (
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/permission"
)

// PermissionKeyTranscripts gates all Muse monitoring.
const PermissionKeyTranscripts = "transcripts"

// Source is the adapter's transcript-tree declaration. Muse's session root
// is a plain $HOME-relative constant (defaultRootDir; see its doc comment
// for what's unverified about relocation), so the static Dir field
// suffices — no DirFunc is needed, mirroring junie. The session ID comes
// from the <session-id> directory (sessionIDFromPath) since the transcript
// filename is the constant session.jsonl; the same function also derives
// nested-subagent parent linkage (ParentSessionIDFromPath) and suppresses
// the top-level approval-only shadow directory format-spec §1 documents —
// see adapter.go for all three.
//
// session-index.db, the sidecar Muse also writes next to the sessions
// root, is deliberately NOT read anywhere in this adapter: format-spec §6
// measured it unreliably synced with the live session set (2 of 14
// sessions indexed at read time, one of those carrying placeholder-only
// data — status "missing_metadata", model_id "", prompt_count 0 for an
// actively-running session with 400+ records). session.jsonl is the only
// store this adapter treats as authoritative.
func Source() agent.Source {
	return agent.FilesUnderRoot{
		Dir:                     defaultRootDir,
		SessionIDFromPath:       sessionIDFromPath,
		ParentSessionIDFromPath: parentSessionIDFromPath,
		Parser: agent.JSONLineParser{
			NewParser: func() agent.LineParser { return &Parser{} },
		},
	}
}

// Agent returns the Meta Muse adapter registration. The CommandPattern
// matcher binds the version-pinned muse-bin-<version>-R<build> binary
// (processCmdPattern; format-spec §2), and DiscoverPID keys off each
// session's own .session.lock file rather than a process-name scan, since
// one muse process can legitimately serve several sessions at once (`muse
// serve`, format-spec §2) — see pid.go.
//
// Muse exposes no documented native hook/callback mechanism: format-spec's
// research (covering `muse --help` and the MSP wire schema) surfaced
// nothing resembling one. UNVERIFIED beyond that — a dedicated
// hook-discovery pass (e.g. exhaustively reading muse's own docs for a
// callback/plugin system) was out of this stage's scope, so this is a
// negative finding from the research actually done, not a claim the
// mechanism is proven absent. So for now the adapter is observe-only, the
// junie/aider shape: a single transcripts permission and no hooks. If a
// native hook mechanism does turn out to exist, it belongs in a later
// stage as a separate modify-kind permission.
func Agent() agent.Agent {
	return agent.Agent{
		Identity: agent.Identity{
			Name:         AdapterName,
			DisplayName:  "Muse",
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
				Touches: "Reads session transcripts under ~/.local/share/muse/sessions/, " +
					"the small .session.lock file next to each transcript, and the " +
					"working directory of running muse processes",
				Detail: "Tails session.jsonl files under " +
					"~/.local/share/muse/sessions/<YYYY>/<MM>/<DD>/<session-id>/ to " +
					"derive session state, activity, and timeline, including nested " +
					"subagent session.jsonl files under each session's subagent/ " +
					"directory. Reads each session's small .session.lock file — an " +
					"advisory lock, not conversation content — to bind a live " +
					"session to its process ID. Also scans for running muse " +
					"processes. Read-only — no file is ever modified, and " +
					"session-index.db is never read. Toggling off stops all " +
					"reading immediately.",
			},
		},
	}
}
