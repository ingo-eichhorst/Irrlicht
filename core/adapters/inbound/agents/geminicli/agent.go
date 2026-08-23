package geminicli

import (
	"regexp"
	"strings"

	"irrlicht/core/adapters/inbound/agents/hookjson"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/permission"
)

// PermissionKeyTranscripts gates all Gemini CLI monitoring (issue #570).
const PermissionKeyTranscripts = "transcripts"

// commandPattern matches the Gemini CLI command line. Gemini runs as
// `node .../bin/gemini` (and re-execs a worker with `--max-old-space-size`),
// so neither process is named "gemini" at the OS level — we match the script
// path in argv instead. Passed verbatim to `pgrep -f`, so it stays a plain,
// portable substring (no \b / lookaround that BSD pgrep would choke on).
var commandPattern = regexp.MustCompile(`bin/gemini`)

// Gemini's signature four-point spark. Light/dark variants swap the fill so
// the mark reads against either chrome: Google blue on light, the lighter
// dark-mode blue on dark.
const iconSVGLight = `<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 100 100">
  <path d="M50 6 C54 30 70 46 94 50 C70 54 54 70 50 94 C46 70 30 54 6 50 C30 46 46 30 50 6 Z" fill="#1A73E8"/>
</svg>`

const iconSVGDark = `<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 100 100">
  <path d="M50 6 C54 30 70 46 94 50 C70 54 54 70 50 94 C46 70 30 54 6 50 C30 46 46 30 50 6 Z" fill="#8AB4F8"/>
</svg>`

// Source is the adapter's transcript-tree declaration.
//
// The hook receiver's path confiner reads it per request (issue #1361), and
// Agent() below is its only other caller — one declaration, so the tree the
// daemon watches and the tree the receiver confines to cannot drift apart.
func Source() agent.Source {
	return agent.FilesUnderRoot{
		Dir: sessionsDir(),
		Parser: agent.JSONLineParser{
			NewParser: func() agent.LineParser { return &Parser{} },
		},
	}
}

// Agent returns the Gemini CLI adapter registration.
func Agent() agent.Agent {
	return agent.Agent{
		Identity: agent.Identity{
			Name:         AdapterName,
			DisplayName:  "Gemini CLI",
			IconSVGLight: iconSVGLight,
			IconSVGDark:  iconSVGDark,
		},
		Process: agent.Process{
			Match:         agent.CommandPattern{Regex: commandPattern},
			PIDForSession: DiscoverPID,
			ExcludeArgv:   isHeapBumpWorker,
		},
		Source:  Source(),
		Control: agent.Control{SupportsInput: true, Interrupt: agent.InterruptCtrlC},
		Permissions: []agent.Permission{
			agent.ControlPermission(),
			{
				Key:             PermissionKeyTranscripts,
				Kind:            permission.KindObserve,
				Title:           "Read session transcripts",
				FeatureUnlocked: "Session list, timeline, cost & token metrics",
				Touches: "Reads session transcripts under ~/.gemini/tmp/ and basic " +
					"info (working directory, command line) of running gemini processes",
				Detail: "Tails *.jsonl session files under ~/.gemini/tmp/<project>/chats/ " +
					"to derive session state, cost, and token metrics. Also scans for " +
					"running gemini processes and reads their working directory and " +
					"command line — to bind a session to its process and skip the " +
					"Node heap-bump worker. Read-only — no file is ever modified. " +
					"Toggling off stops all reading immediately.",
			},
			{
				Key:             PermissionKeyHooks,
				Kind:            permission.KindModify,
				Title:           "Install status hooks",
				FeatureUnlocked: "Authoritative turn-end detection, and an instant permission-prompt refresh",
				// Count and event list are derived from installedHookEvents,
				// never restated: this copy is the consent contract, and
				// hand-maintaining it is how claudecode's came to promise six
				// entries against a seven-event install (#1356).
				Touches: hookjson.EntriesTouched("~/.gemini/settings.json", installedHookEvents),
				Detail: "Adds " + hookjson.EventList(installedHookEvents) +
					" hook entries to ~/.gemini/settings.json (Gemini CLI's own settings " +
					"file — every other key you have there is left untouched). Gemini CLI " +
					"has no HTTP hook delivery, so each entry runs `irrlichd hook-post " +
					"gemini-cli` — a tiny command irrlicht ships — which reads the daemon's " +
					"own published address at the moment the hook fires and forwards the " +
					"payload; it never blocks a tool call even when the daemon is not " +
					"running. " +
					hookjson.RequiresVersion("Gemini CLI", minCLIVersion) +
					" Toggling off removes exactly these entries (also available via " +
					"`irrlichd --uninstall-hooks`).",
				Apply:  func() error { _, err := EnsureHooksInstalled(); return err },
				Remove: func() error { _, err := UninstallHooks(); return err },
				Writes: &agent.ManagedUserFile{
					Path:      geminiSettingsPath,
					Uninstall: UninstallHooks,
					// Read-only; the repair it feeds runs through
					// PermissionService, which re-checks consent under its own
					// lock before writing (#1372).
					Verify: VerifyHooksInstalled,
					Version: &agent.VersionGate{
						Min:   minCLIVersion,
						Probe: []string{"gemini", "--version"},
					},
					// The maintainer's own real ~/.gemini/settings.json (issue
					// #1756) — see hookinstaller_realconfig_test.go.
					RealFixture: &agent.RealConfigFixture{
						Path:       "testdata/real-config-0.56.0.json",
						CLIVersion: "0.56.0",
					},
				},
			},
		},
	}
}

// isHeapBumpWorker reports whether a matched process is Gemini CLI's Node
// worker rather than the launcher. The launcher (`node .../bin/gemini`)
// re-execs a child with a larger V8 heap (`node --max-old-space-size=…
// .../bin/gemini`); both match commandPattern and share the workspace cwd, so
// without this filter each session would mint two pre-sessions and strand one
// as a ghost. Excluding the worker leaves exactly the launcher. DiscoverPID
// applies this same predicate before disambiguating (see pid.go), so the
// scanner and PID discovery agree on binding the launcher, not the worker.
//
// Per the ExcludeArgv contract a nil/unreadable argv must NOT be excluded;
// the loop over an empty slice naturally returns false. A future single-
// process Gemini (no heap-bump re-exec) is also handled: with no such flag
// the lone process is kept.
func isHeapBumpWorker(argv []string) bool {
	for _, a := range argv {
		if strings.HasPrefix(a, "--max-old-space-size") {
			return true
		}
	}
	return false
}
