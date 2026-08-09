package claudecode

import (
	"irrlicht/core/adapters/inbound/agents/hookjson"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/backchannel"
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
	// One name, used both as the wizard's label and in the hooks permission's
	// version disclosure below, so the copy cannot come to call the CLI
	// something the rest of the UI doesn't.
	const displayName = "Claude Code"
	return agent.Agent{
		Identity: agent.Identity{
			Name:         AdapterName,
			DisplayName:  displayName,
			IconSVGLight: iconSVG,
			IconSVGDark:  iconSVG,
		},
		Process: agent.Process{
			Match:         agent.ExactName{Name: ProcessName},
			PIDForSession: DiscoverPID,
			ExcludeArgv:   IsInfraArgv,
		},
		Source: Source(),
		Control: agent.Control{
			SupportsInput: true,
			Interrupt:     agent.InterruptCtrlC,
			Presets: map[string]string{
				backchannel.PresetCompact: "/compact",
			},
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
				// Count and event list are derived from installedHookEvents, never
				// restated: this copy is the consent contract, and hand-maintaining
				// it is how it came to promise six entries against a seven-event
				// install (#1173's Notification hook, undisclosed until #1356).
				Touches: hookjson.EntriesTouched("~/.claude/settings.json", installedHookEvents),
				Detail: "Adds " + hookjson.EventList(installedHookEvents) +
					" hook entries that POST " +
					"the hook payload to the local daemon at " + hookEndpointURL() +
					" via Claude Code's native http hook (no shell/curl). " +
					hookjson.RequiresVersion(displayName, minCLIVersion) +
					" Toggling off removes exactly these entries (also available via " +
					"`irrlichd --uninstall-hooks`).",
				Apply:  func() error { _, err := EnsureHooksInstalled(); return err },
				Remove: func() error { _, err := UninstallHooks(); return err },
				Hooks: &agent.HookInstall{
					ConfigPath: claudeSettingsPath,
					Uninstall:  UninstallHooks,
					// Read-only; the repair it feeds runs through
					// PermissionService, which re-checks consent under its own
					// lock before writing (#1372).
					Verify: VerifyHooksInstalled,
					// Declared, not implemented — PermissionService enforces it
					// (#1365). Observed reads the version Claude Code stamps on
					// every transcript line, which is what makes this gate real
					// rather than decorative: the production daemon inherits a
					// LaunchServices PATH that does not include ~/.local/bin, so
					// the probe alone would miss and every install would take the
					// fail-open branch. The probe stays as the fallback and as the
					// authority whenever the passive reading says "too old".
					Version: &agent.VersionGate{
						Min:      minCLIVersion,
						Probe:    []string{"claude", "--version"},
						Observed: newestObservedCLIVersion,
					},
				},
			},
			{
				Key:             PermissionKeyStatusline,
				Kind:            permission.KindModify,
				Title:           "Install statusline feed",
				FeatureUnlocked: "Rate-limit / quota forecast for Pro & Max subscriptions",
				Touches:         "Sets statusLine.command in ~/.claude/settings.json",
				Detail: "Sets statusLine.command to: " + installedStatuslineCommand() +
					" — POSTs Claude Code's statusline JSON (carrying rate-limit " +
					"data) to the local daemon. An existing user statusline is " +
					"chained, not replaced. Toggling off restores the previous " +
					"command (or removes the entry if irrlicht installed it).",
				Apply:  func() error { _, err := EnsureStatuslineInstalled(); return err },
				Remove: func() error { _, err := UninstallStatusline(); return err },
			},
			{
				Key:   PermissionKeyInstructions,
				Kind:  permission.KindModify,
				Title: "Install task-progress rule",
				// The block list and count are derived from
				// installedInstructionBlocks — the same slice Apply iterates —
				// never restated here. Hand-maintaining this text is how it came
				// to disclose the task-summary block retired in #1186 while
				// saying nothing about the task-question block (#759) it has
				// been writing to the user's CLAUDE.md ever since (#1377).
				FeatureUnlocked: "Task-completion ETA chip + waiting-question headline from agent-reported markers",
				Touches:         instructionsTouched(),
				Detail:          instructionsDetail(),
				Apply:           applyInstructionBlocks,
				Remove:          removeInstructionBlocks,
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

// Source is this adapter's transcript-source declaration, split out of Agent so
// callers that need only the roots do not rebuild the whole declaration.
//
// Claude Code transcripts live one file per session under
// $CLAUDE_CONFIG_DIR/projects (default ~/.claude/projects).
//
// The hook receiver's path confiner reads it per request (issue #1361), and
// Agent() below is its only other caller — one declaration, so the tree the
// daemon watches and the tree the receiver confines to cannot drift apart.
func Source() agent.Source {
	return agent.FilesUnderRoot{
		Dir: transcriptsDir(),
		Parser: agent.JSONLineParser{
			NewParser: func() agent.LineParser { return &Parser{} },
		},
	}
}
