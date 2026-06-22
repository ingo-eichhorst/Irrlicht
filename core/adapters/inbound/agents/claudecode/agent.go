package claudecode

import (
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/permission"
	"irrlicht/core/pkg/tailer"
)

// Permission keys for the consent wizard (issue #570). Referenced by the
// hook/statusline HTTP handlers' consent gates and the daemon wiring.
const (
	PermissionKeyHooks        = "hooks"
	PermissionKeyStatusline   = "statusline"
	PermissionKeyTranscripts  = "transcripts"
	PermissionKeyInstructions = "instructions"
)

// Claude Code mascot — pixel-art rectangular creature with eyes and legs.
// The brand orange (#D97757) reads well in both light and dark themes,
// so the same markup serves both appearances.
const iconSVG = `<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 56 56">
  <rect x="8" y="4" width="40" height="32" rx="4" fill="#D97757"/>
  <rect x="4" y="16" width="8" height="12" rx="2" fill="#D97757"/>
  <rect x="44" y="16" width="8" height="12" rx="2" fill="#D97757"/>
  <rect x="18" y="12" width="8" height="8" rx="1" fill="#4A2820"/>
  <rect x="30" y="12" width="8" height="8" rx="1" fill="#4A2820"/>
  <rect x="12" y="36" width="6" height="14" rx="1" fill="#D97757"/>
  <rect x="22" y="36" width="6" height="10" rx="1" fill="#D97757"/>
  <rect x="32" y="36" width="6" height="10" rx="1" fill="#D97757"/>
  <rect x="42" y="36" width="6" height="14" rx="1" fill="#D97757"/>
</svg>`

// Agent returns the Claude Code adapter registration.
func Agent() agent.Agent {
	return agent.Agent{
		Identity: agent.Identity{
			Name:         AdapterName,
			DisplayName:  "Claude Code",
			IconSVGLight: iconSVG,
			IconSVGDark:  iconSVG,
		},
		Process: agent.Process{
			Match:         agent.ExactName{Name: ProcessName},
			PIDForSession: DiscoverPID,
			ExcludeArgv:   IsInfraArgv,
		},
		Source: agent.FilesUnderRoot{
			Dir: transcriptsDir(),
			Parser: agent.JSONLineParser{
				NewParser: func() agent.LineParser { return &Parser{} },
			},
		},
		Control: agent.Control{
			SupportsInput: true,
			Interrupt:     agent.InterruptCtrlC,
		},
		Permissions: []agent.Permission{
			{
				Key:             PermissionKeyTranscripts,
				Kind:            permission.KindObserve,
				Title:           "Read session transcripts",
				FeatureUnlocked: "Session list, timeline, cost & token metrics",
				Touches: "Reads session transcripts under ~/.claude/projects/ and " +
					"basic info (working directory, command line) of running claude processes",
				Detail: "Tails *.jsonl transcript files under ~/.claude/projects/ " +
					"to derive session state, cost, and token metrics. Also scans " +
					"for running claude processes and reads their working directory " +
					"and command line — to show a session before its first message " +
					"and to skip Claude Code's background daemon processes. " +
					"Read-only — no transcript file is ever modified. Toggling off " +
					"stops all reading immediately.",
			},
			{
				Key:             PermissionKeyHooks,
				Kind:            permission.KindModify,
				Title:           "Install status hooks",
				FeatureUnlocked: "Instant waiting-state detection (permission prompts, plan approval, questions)",
				Touches:         "Writes 4 hook entries to ~/.claude/settings.json",
				Detail: "Adds PermissionRequest, PreToolUse, PostToolUse, and " +
					"PostToolUseFailure hook entries whose command is: " +
					installedHookCommand + " — each POSTs the hook payload to the " +
					"local daemon. Toggling off removes exactly these entries " +
					"(also available via `irrlichd --uninstall-hooks`).",
				Apply:  func() error { _, err := EnsureHooksInstalled(); return err },
				Remove: func() error { _, err := UninstallHooks(); return err },
			},
			{
				Key:             PermissionKeyStatusline,
				Kind:            permission.KindModify,
				Title:           "Install statusline feed",
				FeatureUnlocked: "Rate-limit / quota forecast for Pro & Max subscriptions",
				Touches:         "Sets statusLine.command in ~/.claude/settings.json",
				Detail: "Sets statusLine.command to: " + installedStatuslineCommand +
					" — POSTs Claude Code's statusline JSON (carrying rate-limit " +
					"data) to the local daemon. An existing user statusline is " +
					"chained, not replaced. Toggling off restores the previous " +
					"command (or removes the entry if irrlicht installed it).",
				Apply:  func() error { _, err := EnsureStatuslineInstalled(); return err },
				Remove: func() error { _, err := UninstallStatusline(); return err },
			},
			{
				Key:             PermissionKeyInstructions,
				Kind:            permission.KindModify,
				Title:           "Install task-progress rule",
				FeatureUnlocked: "Task-completion ETA chip from agent-reported progress",
				Touches:         "Maintains a managed block in ~/.claude/CLAUDE.md",
				Detail: "Writes an irrlicht-managed block (between BEGIN/END " +
					"sentinels) to ~/.claude/CLAUDE.md instructing the agent to " +
					"periodically emit a hidden task-progress marker, which " +
					"irrlicht reads from the transcript to project a completion " +
					"ETA. All surrounding file content is preserved " +
					"byte-for-byte. Toggling off removes exactly this block " +
					"(also available via the macOS Settings toggle).",
				Apply:  func() error { _, err := EnsureTaskEtaBlockInstalled(); return err },
				Remove: func() error { _, err := UninstallTaskEtaBlock(); return err },
			},
			agent.ControlPermission(),
		},
	}
}

// OpenSubagents satisfies agent.SubagentCounter so the metrics collector
// can discover the subagent count via type assertion on the LineParser
// returned by JSONLineParser.NewParser. The actual counting is a pure
// function of SessionMetrics and lives in CountOpenSubagents.
func (p *Parser) OpenSubagents(m *tailer.SessionMetrics) int {
	return CountOpenSubagents(m)
}
