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
			// Every session no client runs binds to the one 2.x service
			// (#2107), so its roots share that PID while their sessions last;
			// a client's PID stays exclusive (OwnsSharedPID).
			SharedPIDOwner: OwnsSharedPID,
			// SessionHostArgv rather than ExcludeArgv, as codex's app-server
			// (#2088): the 2.x service hosts sessions rather than being
			// infrastructure nothing binds to — since #2107 a session no
			// client runs binds to it (DiscoverPID). Declaring it keeps the
			// scanner from minting a row for the service, and, with no
			// ReleasedPID declared, lets the ready-session TTL end an idle root
			// the live service still hosts (PIDManager.reapsIdleTopLevel). With
			// no LauncherPID declared, a service-bound root captures no
			// launcher — the service's env is whichever client spawned it — and
			// opencode stays out of the hosted-placeholder pairing (#2082),
			// which needs one: a service-bound root has no client to have left
			// a placeholder behind, and a client's own session binds to the
			// client.
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
					"directory of processes named opencode, opencode2 or opencode.exe. " +
					"The command line is also how it finds OpenCode 2.x's background " +
					"service (`opencode serve --service`), which hosts the sessions no " +
					"terminal client runs — those made through its API, the desktop app " +
					"or the web app. " +
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
				// "3 hook entries" and "5 hook entries" are what
				// contracttesting's #1356 count arm reads — one count per
				// artifact, each the number of event subscriptions that
				// artifact holds (#2108). They live in ONE file, which is the
				// unusual part, so Touches names the shape in the same breath
				// rather than leaving it to Detail — the wizard row is what
				// most users read.
				Touches: "Writes 3 hook entries (opencode 1.x) or 5 hook entries (opencode 2.x) to " +
					displayPluginPath + " — a small JavaScript file that opencode loads and runs",
				Detail: hooksDetail(),
				// Apply and Verify are the 1.x artifact's. PermissionService
				// never runs them for this declaration — it runs the variant
				// the installed version selects (Writes.Variants, #2108) — but
				// the registry tripwires require them, the managed-writes
				// grader and the gating contract run them directly, and the 1.x
				// artifact is the one whose shape every one of those predates.
				Apply:  applyV1,
				Remove: func() error { _, err := UninstallHooks(); return err },
				Writes: &agent.ManagedUserFile{
					Path:      PluginPath,
					Uninstall: UninstallHooks,
					Verify:    VerifyHooksInstalled,
					// The envelope of the two variants below: the 1.x floor
					// and the 2.x ceiling. Observed is the store, read only
					// while the database permission is granted; the probe is
					// the fallback.
					Version: &agent.VersionGate{
						Min:              minOpenCodeVersion,
						Max:              maxOpenCodeV2Version,
						Probe:            []string{"opencode", "--version"},
						Observed:         newestObservedCLIVersion,
						ObservedRequires: PermissionKeyDatabase,
					},
					Variants: []agent.HookVariant{
						{
							Min:    minOpenCodeVersion,
							Max:    maxOpenCodeVersion,
							Events: installedHookEvents,
							Since:  hookEventSince,
							Apply:  applyV1,
							Verify: VerifyHooksInstalled,
						},
						{
							Min:    minOpenCodeV2Version,
							Max:    maxOpenCodeV2Version,
							Events: installedHookEventsV2,
							Since:  hookEventSinceV2,
							Apply:  func() error { _, err := EnsureHooksInstalledV2(); return err },
							Verify: VerifyHooksInstalledV2,
						},
					},
				},
			},
		},
	}
}

// applyV1 installs the 1.x artifact: the permission's own Apply and the 1.x
// variant's, one closure so the two cannot drift.
func applyV1() error { _, err := EnsureHooksInstalled(); return err }

// hooksDetail is the hooks permission's expanded consent copy. A function
// because its version sentences are rendered from the same constants the gate
// compares against (#1365, #2105, #2108), so the copy cannot drift from it.
func hooksDetail() string {
	return "opencode has no hooks section in its config: its experimental " +
		"settings carry no hook key, and the only way to observe its lifecycle " +
		"is a plugin, so this permission installs one. That is a single " +
		"JavaScript file at " + displayPluginPath + ", which opencode " +
		"auto-discovers and runs with your own permissions. It is code irrlicht " +
		"ships in its own binary, not a package fetched from anywhere — no npm, " +
		"no git, no build step — and opencode's own config file is not touched " +
		"at all. opencode 1.x and 2.x load different plugin formats, so which " +
		"file is written depends on the opencode you run, and an upgrade or a " +
		"downgrade rewrites it in place: " +
		"for opencode " + minOpenCodeVersion + " or newer and older than " + maxOpenCodeVersion +
		" it is the 1.x plugin, which runs inside every opencode session and subscribes to " +
		hookjson.EventList(installedHookEvents) + "; " +
		"for opencode " + minOpenCodeV2Version + " or newer and older than " + maxOpenCodeV2Version +
		" it is the 2.x plugin, which runs inside opencode's shared background service " +
		"(`opencode serve --service`) rather than in each session, and subscribes to " +
		hookjson.EventList(installedHookEventsV2) + ". " +
		"Either file imports only node:child_process and does one thing with each " +
		"event: runs `irrlichd hook-post opencode`, a tiny command irrlicht ships, " +
		"handing it the session id — and, from the 2.x plugin, the event's own id, " +
		"because the service delivers each event once per project it has open and " +
		"the daemon keeps one. That command reads the daemon's own published " +
		"address at the moment the event fires, so nothing in the file names a " +
		"host or a port and it cannot go stale; it also never blocks opencode, " +
		"even when the daemon is not running. Neither file registers anything but " +
		"opencode's read-only event subscription — never a hook that could answer " +
		"a permission prompt for you or change what opencode does. " +
		"To tell which opencode you run, irrlicht reads the version opencode " +
		"recorded on the newest session in its database — only while reading that " +
		"database is granted — and otherwise runs `opencode --version`. " +
		"On a version older than " + minOpenCodeVersion + " nothing is written. On " +
		maxOpenCodeV2Version + " or newer, or on a 2.x older than " + minOpenCodeV2Version +
		", nothing is written and the file an earlier install left there is removed. " +
		"When the version cannot be read at all, nothing is written, because a wrong " +
		"guess writes the file the other major rejects. Either way the daemon " +
		"reports why. Toggling off deletes the file (also available via " +
		"`irrlichd --uninstall-hooks`); nothing else in your opencode " +
		"installation was changed, so there is nothing else to undo."
}

// metricsReader adapts the package-level ComputeMetrics function to the
// agent.MetricsReader interface.
type metricsReader struct{}

func (metricsReader) ComputeMetrics(storePath, sessionID string) (*session.SessionMetrics, error) {
	return ComputeMetrics(storePath, sessionID)
}
