package codex

import (
	"irrlicht/core/adapters/inbound/agents/hookjson"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/permission"
)

// PermissionKeyTranscripts gates all Codex monitoring (issue #570).
const PermissionKeyTranscripts = "transcripts"

// PermissionKeyHooks gates installing Codex hooks into ~/.codex/hooks.json for
// the hook-authoritative live-state tier (issue #1171). Aliased rather than
// spelled out: `irrlichd --uninstall-hooks` narrows the managed-file projection
// to this key from outside the adapter (#1383), so a local literal is a string
// two packages agree on by convention.
const PermissionKeyHooks = agent.HooksPermissionKey

// Codex — circle with >_ terminal prompt. Color picks contrast against
// the surrounding chrome: near-black on light themes, near-white on dark.
const iconSVGLight = `<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 100 100">
  <circle cx="50" cy="50" r="44" fill="none" stroke="#1A1A1A" stroke-width="8"/>
  <path d="M28 38 L42 50 L28 62" fill="none" stroke="#1A1A1A" stroke-width="7" stroke-linecap="round" stroke-linejoin="round"/>
  <line x1="48" y1="62" x2="68" y2="62" stroke="#1A1A1A" stroke-width="7" stroke-linecap="round"/>
</svg>`

const iconSVGDark = `<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 100 100">
  <circle cx="50" cy="50" r="44" fill="none" stroke="#E0E0E0" stroke-width="8"/>
  <path d="M28 38 L42 50 L28 62" fill="none" stroke="#E0E0E0" stroke-width="7" stroke-linecap="round" stroke-linejoin="round"/>
  <line x1="48" y1="62" x2="68" y2="62" stroke="#E0E0E0" stroke-width="7" stroke-linecap="round"/>
</svg>`

// Agent returns the Codex adapter registration.
func Agent() agent.Agent {
	// See claudecode's Agent(): one name for the wizard label and the version
	// disclosure, so the two cannot drift.
	const displayName = "Codex"
	return agent.Agent{
		Identity: agent.Identity{
			Name:         AdapterName,
			DisplayName:  displayName,
			IconSVGLight: iconSVGLight,
			IconSVGDark:  iconSVGDark,
		},
		Process: agent.Process{
			Match:          agent.ExactName{Name: ProcessName},
			PIDForSession:  DiscoverPID,
			SharedPIDOwner: OwnsSharedPID,
			ReleasedPID:    ReleasedPID,
			// Not ExcludeArgv: every codex root is bound to an app-server
			// (#2077), and the #727 infra reaper ends a session bound to a
			// process ExcludeArgv rejects.
			SessionHostArgv: IsAppServerArgv,
			// A hosted root's launcher comes from its TUI when that TUI is
			// the only one in the root's cwd (#2083).
			LauncherPID: LauncherPID,
		},
		Source: Source(),
		Permissions: []agent.Permission{
			{
				Key:             PermissionKeyTranscripts,
				Kind:            permission.KindObserve,
				Title:           "Read session transcripts",
				FeatureUnlocked: "Session list, timeline, cost & token metrics",
				Touches:         "Reads session transcripts under ~/.codex/sessions/ and basic process data, including codex process arguments to tell app-server processes from sessions and codex working directories to find the terminal a hosted session runs in; with DSH consent, reads parent PIDs to identify DSH-owned one-shot children",
				Detail: "Tails *.jsonl session files under ~/.codex/sessions/YYYY/MM/DD/ " +
					"to derive session state, cost, and token metrics. Scans Codex processes " +
					"to show sessions before their first message, reading each one's arguments " +
					"so app-server processes get no row, and reads their working " +
					"directories to tie a session hosted by the shared app-server to " +
					"the one terminal running codex in its directory. With DSH consent, checks " +
					"process arguments and parent PIDs to avoid duplicate one-shot child rows. Read-only — " +
					"no file is ever modified. Toggling off stops all reading " +
					"immediately.",
			},
			{
				Key:             PermissionKeyHooks,
				Kind:            permission.KindModify,
				Title:           "Install Codex hooks",
				FeatureUnlocked: "Instant waiting/ready detection (approval prompts, turn end)",
				// Derived from installedHookEvents rather than restated — see the
				// same note on claudecode's hooks permission (#1356). Codex's copy
				// happened to be accurate; the contract is what keeps it so.
				Touches: hookjson.EntriesTouched("~/.codex/hooks.json", installedHookEvents),
				Detail: "Adds " + hookjson.EventList(installedHookEvents) + " hooks to " +
					"~/.codex/hooks.json (a dedicated file, never config.toml) that " +
					"POST the hook payload to the local daemon at " + hookEndpointURL() +
					" via curl. PermissionRequest drives a live waiting transition " +
					"the moment an approval prompt appears; Stop carries the final " +
					"assistant message for turn-end. " + hookjson.RequiresVersion(displayName, minCLIVersion) +
					" Toggling off removes the entries.",
				Apply:  func() error { _, err := EnsureHooksInstalled(); return err },
				Remove: func() error { _, err := UninstallHooks(); return err },
				Writes: &agent.ManagedUserFile{
					Path:      codexHooksPath,
					Uninstall: UninstallHooks,
					// Read-only; the repair it feeds runs through
					// PermissionService, which re-checks consent under its own
					// lock before writing (#1372).
					Verify: VerifyHooksInstalled,
					// The floor is declared, not implemented: PermissionService
					// enforces it for every adapter (#1365). Observed reads
					// cli_version out of the newest session header, so the common
					// case costs no process at all and works even when the binary
					// is not on the daemon's PATH; Probe is the fallback.
					Version: &agent.VersionGate{
						Min:      minCLIVersion,
						Probe:    []string{"codex", "--version"},
						Observed: newestObservedCLIVersion,
					},
				},
			},
		},
	}
}

// IsAppServerArgv reports whether a `codex`-binary process is a codex
// app-server rather than a TUI a user talks to (#2082): an argument after
// argv[0] equal to "app-server" that is not an option's value. The shapes it
// covers, read with `ps -o args=`
// on the dev machine on 2026-10-11:
//
//   - `codex app-server --listen unix:// --analytics-default-enabled --managed-daemon`
//     (the shared daemon that hosts every TUI's threads, #2077)
//   - `codex app-server daemon pid-update-loop` (that daemon's supervisor)
//   - `codex -c features.code_mode_host=true app-server --analytics-default-enabled`
//     (the VS Code ChatGPT extension's app-server)
//
// A prompt is a single argv slot, so a TUI started with a prompt that merely
// mentions the word is not matched; a TUI whose whole prompt is exactly
// `app-server` would be. Nor is the value of one of valueOptions matched (a TUI
// started with `--cd app-server` for a package directory of that name); the
// value of an option missing from that list still is. A nil or empty argv
// (unreadable) is never an app-server.
//
// Declared as codex's Process.SessionHostArgv: the scanner mints no
// placeholder row for an app-server, and a root bound to one retires its TUI's
// placeholder. Exported for #2083.
func IsAppServerArgv(argv []string) bool {
	for i := 1; i < len(argv); i++ {
		if argv[i] == "app-server" && !valueOptions[argv[i-1]] {
			return true
		}
	}
	return false
}

// valueOptions are the codex options that take their value as the next
// argument: every option `codex --help` lists with a value placeholder at
// 0.162.1, under each name it accepts (read 2026-10-11 for #2083).
//
// `-i/--image <FILE>...` takes several values, of which only the first is
// skipped here, so `codex -i a.png app-server` still reads as an app-server.
var valueOptions = map[string]bool{
	"-c": true, "--config": true, "--enable": true, "--disable": true,
	"--remote": true, "--remote-auth-token-env": true,
	"-i": true, "--image": true, "-m": true, "--model": true,
	"--local-provider": true, "-p": true, "--profile": true,
	"-s": true, "--sandbox": true, "-C": true, "--cd": true,
	"--add-dir": true, "-a": true, "--ask-for-approval": true,
}

// Source is this adapter's transcript-source declaration, split out of Agent so
// callers that need only the roots do not rebuild the whole declaration.
//
// Codex rollouts live under $CODEX_HOME/sessions (default
// ~/.codex/sessions), nested sessions/YYYY/MM/DD/*.jsonl.
//
// The hook receiver's path confiner reads it per request (issue #1361), and
// Agent() below is its only other caller — one declaration, so the tree the
// daemon watches and the tree the receiver confines to cannot drift apart.
func Source() agent.Source {
	return agent.FilesUnderRoot{
		Dir:                     sessionsDir(),
		SessionIDFromPath:       sessionIDFromPath,
		ParentSessionIDFromPath: parentSessionIDFromPath,
		Parser: agent.JSONLineParser{
			NewParser: func() agent.LineParser { return &Parser{} },
		},
	}
}
