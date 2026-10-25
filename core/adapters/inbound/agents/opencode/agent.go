package opencode

import (
	"irrlicht/core/adapters/inbound/agents/hookjson"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/permission"
	"irrlicht/core/domain/session"
)

// PermissionKeyDatabase gates all OpenCode monitoring (issue #570).
const PermissionKeyDatabase = "database"

// OpenCode — curly braces { } in a circle, rendered in OpenCode's brand blue.
// Background swaps for theme so the icon doesn't blow out against light or
// dark surrounding chrome; foreground stays brand blue (#3B82F6) in both.
const iconSVGLight = `<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 100 100">
  <circle cx="50" cy="50" r="44" fill="#F0F6FF" stroke="#3B82F6" stroke-width="6"/>
  <path d="M42 28 Q30 28 30 38 L30 46 Q30 50 26 50 Q30 50 30 54 L30 62 Q30 72 42 72" fill="none" stroke="#3B82F6" stroke-width="7" stroke-linecap="round" stroke-linejoin="round"/>
  <path d="M58 28 Q70 28 70 38 L70 46 Q70 50 74 50 Q70 50 70 54 L70 62 Q70 72 58 72" fill="none" stroke="#3B82F6" stroke-width="7" stroke-linecap="round" stroke-linejoin="round"/>
</svg>`

const iconSVGDark = `<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 100 100">
  <circle cx="50" cy="50" r="44" fill="#0D1117" stroke="#3B82F6" stroke-width="6"/>
  <path d="M42 28 Q30 28 30 38 L30 46 Q30 50 26 50 Q30 50 30 54 L30 62 Q30 72 42 72" fill="none" stroke="#3B82F6" stroke-width="7" stroke-linecap="round" stroke-linejoin="round"/>
  <path d="M58 28 Q70 28 70 38 L70 46 Q70 50 74 50 Q70 50 70 54 L70 62 Q70 72 58 72" fill="none" stroke="#3B82F6" stroke-width="7" stroke-linecap="round" stroke-linejoin="round"/>
</svg>`

// OpenCode is the only currently-supported adapter using ProcessOwnedStore
// — session state lives in a SQLite database rather than JSONL files.
// The daemon's wiring today drives metric computation through Reader using
// the agent.Event.TranscriptPath supplied by the dedicated opencode
// watcher (constructed inline in cmd/irrlichd/main.go); PathForPID is not
// yet called.
func Agent() agent.Agent {
	return agent.Agent{
		Identity: agent.Identity{
			Name:         AdapterName,
			DisplayName:  "OpenCode",
			IconSVGLight: iconSVGLight,
			IconSVGDark:  iconSVGDark,
		},
		Process: agent.Process{
			// A command-line pattern rather than an exact name: an opencode
			// 2.x client can run as `opencode`, `opencode2` or `opencode.exe`
			// (processCmdPattern, #2106).
			Match:         agent.CommandPattern{Regex: processCmdRegex},
			PIDForSession: DiscoverPID,
			// SessionHostArgv rather than ExcludeArgv, as codex's app-server
			// (#2088): the 2.x service hosts sessions rather than being
			// infrastructure nothing binds to (#2104's P3 binds service-hosted
			// sessions to it). Declaring it also opts opencode into
			// PIDManager's hosted-placeholder pairing (hostGroups /
			// pairHostedRoots, #2082), which acts only on a root whose bound
			// PID's argv is the service. In #2105 no root gets there, by
			// reading rather than by a test: 1.x roots bind by cwd through
			// DiscoverPID, and the 1.x reader stands down on any store
			// carrying session_v2, which a 2.x service's store does. Since
			// #2106 the 2.x reader's roots bind through DiscoverPID as well,
			// which excludes the service, so still no root binds to it.
			// boundToSessionHost also needs a LauncherPID, which opencode
			// does not declare.
			SessionHostArgv: IsServiceArgv,
		},
		Source: agent.ProcessOwnedStore{
			// Panic rather than return "" so a future caller that wires
			// this in fails loudly; the silent-empty path would have given
			// ComputeMetrics an empty filename and produced no rows.
			PathForPID: func(int) string {
				panic("opencode.Agent().Source.PathForPID: resolver not installed")
			},
			Reader: metricsReader{},
		},
		Permissions: []agent.Permission{
			{
				Key:             PermissionKeyDatabase,
				Kind:            permission.KindObserve,
				Title:           "Read session database",
				FeatureUnlocked: "Session list, timeline, cost & token metrics",
				Touches:         "Reads OpenCode's database — ~/.local/share/opencode/opencode.db, or where $OPENCODE_DB or $XDG_DATA_HOME puts it (read-only polling) — the command line of running opencode processes, and the type and account-id claim of the openai-oauth entry in ~/.local/share/opencode/auth.json",
				Detail: "Polls OpenCode's SQLite database in read-only mode to derive " +
					"session state, cost, and token metrics. No row is ever written. " +
					"The database is found where OpenCode itself puts it: $OPENCODE_DB " +
					"when set (a relative name is taken inside OpenCode's data directory), " +
					"else opencode.db in $XDG_DATA_HOME/opencode, else " +
					"~/.local/share/opencode/opencode.db. To tell which session belongs " +
					"to which running OpenCode, it reads the command line and working " +
					"directory of processes named opencode, opencode2 or opencode.exe, " +
					"which is also how it leaves OpenCode 2.x's background service " +
					"(`opencode serve --service`) out of that match. " +
					"To share a Codex quota reading with an OpenCode session logged into " +
					"the SAME ChatGPT account, it also reads the openai-oauth block of " +
					"~/.local/share/opencode/auth.json: its type, and the account-id claim " +
					"decoded from its access token. The token itself is not kept, and no " +
					"other provider's entry is used. Toggling off stops all polling immediately.",
			},
			{
				Key:             PermissionKeyHooks,
				Kind:            permission.KindModify,
				Title:           "Install status hooks",
				FeatureUnlocked: "Blocked-on-user detection and authoritative turn-end detection",
				// "3 hook entries" is what contracttesting's #1356 count arm
				// reads, and it is the honest number: three event
				// subscriptions. They live in ONE file, which is the unusual
				// part, so Touches names the shape in the same breath rather
				// than leaving it to Detail — the wizard row is what most users
				// read.
				Touches: "Writes 3 hook entries to " + displayPluginPath +
					" — a small JavaScript file that opencode loads and runs",
				Detail: "opencode has no hooks section in its config: its experimental " +
					"settings carry no hook key, and the only way to observe its lifecycle " +
					"is a plugin, so this permission installs one. That is a single " +
					"JavaScript file at " + displayPluginPath + ", which opencode " +
					"auto-discovers and executes inside every opencode session with your " +
					"own permissions. It is code irrlicht ships in its own binary, not a " +
					"package fetched from anywhere — no npm, no git, no build step — and " +
					"opencode's own config file is not touched at all. The file imports " +
					"only node:child_process, subscribes to exactly three opencode events " +
					"(" + hookjson.EventList(installedHookEvents) + ") and does one thing " +
					"with each: runs `irrlichd hook-post opencode`, a tiny command " +
					"irrlicht ships, handing it the session id. That command reads the " +
					"daemon's own published address at the moment the event fires, so " +
					"nothing in the file names a host or a port and it cannot go stale; it " +
					"also never blocks opencode, even when the daemon is not running. The " +
					"file deliberately registers only opencode's read-only event tap, " +
					"never a hook that could answer a permission prompt for you or change " +
					"what opencode does. " +
					hookjson.RequiresVersionRange("opencode", minOpenCodeVersion, maxOpenCodeVersion) +
					" Toggling off deletes the file (also available via " +
					"`irrlichd --uninstall-hooks`); nothing else in your opencode " +
					"installation was changed, so there is nothing else to undo.",
				Apply:  func() error { _, err := EnsureHooksInstalled(); return err },
				Remove: func() error { _, err := UninstallHooks(); return err },
				Writes: &agent.ManagedUserFile{
					Path:      PluginPath,
					Uninstall: UninstallHooks,
					Verify:    VerifyHooksInstalled,
					Version: &agent.VersionGate{
						Min:   minOpenCodeVersion,
						Max:   maxOpenCodeVersion,
						Probe: []string{"opencode", "--version"},
					},
				},
			},
		},
	}
}

// metricsReader adapts the package-level ComputeMetrics function to the
// agent.MetricsReader interface.
type metricsReader struct{}

func (metricsReader) ComputeMetrics(storePath, sessionID string) (*session.SessionMetrics, error) {
	return ComputeMetrics(storePath, sessionID)
}
