package processlifecycle

import (
	"os"

	"irrlicht/core/adapters/inbound/agents/claudesettings"
	"irrlicht/core/domain/session"
)

// settingsHost is where a Claude Code session's settings files live on this
// machine, apart from the ones under its working directory.
type settingsHost struct {
	Home        string
	ManagedDir  string
	MDMProfiles []string
}

// claudeSettingsHost is the seam tests replace so no test reads this
// machine's real ~/.claude or managed-settings directory.
var claudeSettingsHost = func() settingsHost {
	home, _ := os.UserHomeDir()
	return settingsHost{Home: home, ManagedDir: claudesettings.ManagedDir(), MDMProfiles: claudesettings.MDMProfiles()}
}

// ObserveClaudeCodeRoute is ObserveRoute for a Claude Code session (issue
// #2010). Claude Code applies a settings file's `env` block over the
// environment it inherited, so ANTHROPIC_BASE_URL is resolved from its
// settings layers first, in its documented precedence (package
// claudesettings), and from the process environment only when no layer sets
// it. cwd is the session's working directory, which locates the project and
// project-local files.
//
// The reads all happen under the endpoint-observation permission, and none
// happens while it is not granted: the process's ANTHROPIC_BASE_URL and
// CLAUDE_CONFIG_DIR (endpointEnvKeys), whether its command line carries a
// settings flag (the flag token only), and env.ANTHROPIC_BASE_URL from each
// settings file. No other settings key leaves claudesettings.ReadEnv.
//
// Results beyond ObserveRoute's:
//   - a settings layer that sets the URL: observed or malformed, Source
//     "settings:<layer>", SettingsFile its path. Set to "" it is absent,
//     with the same Source: Claude Code's documentation says an empty value
//     cancels a shell export.
//   - a layer that outranks every hit but cannot be read or reconstructed
//     (an unparseable file, an MDM profile, --settings or --setting-sources,
//     no working directory): unreadable, naming that layer. A lower layer's
//     value is never reported past a layer whose answer is unknown.
//
// The process env is unreadable for a hardened-runtime binary
// (errEnvHidden). That no longer decides the result on its own: a settings
// hit above the user layer is still observed. The user file's location
// depends on the process's CLAUDE_CONFIG_DIR, so with the env unreadable
// that layer is unknown and stops the walk.
func ObserveClaudeCodeRoute(pid int, cwd string, granted bool) *session.RouteObservation {
	if !granted {
		return &session.RouteObservation{Status: session.RouteDenied}
	}
	env, envErr := osProc.EnvOf(pid, endpointEnvKeys)
	argv, argvErr := osProc.ArgvOf(pid)
	host := claudeSettingsHost()
	in := claudesettings.Inputs{
		CWD:              cwd,
		ConfigDir:        env[claudeConfigDirKey],
		ConfigDirUnknown: envErr != nil,
		Home:             host.Home,
		ManagedDir:       host.ManagedDir,
		MDMProfiles:      host.MDMProfiles,
		// ArgvOf answers nil for a command line it could not read (the port
		// contract), and a command line nobody read may carry --settings.
		CLIOpaque: argvErr != nil || len(argv) == 0 || claudesettings.FlagsTouchSettings(argv),
	}
	if obs := routeFromSettings(claudesettings.Lookup(claudesettings.Sources(in), endpointSourceKey)); obs != nil {
		return obs
	}
	return routeFromEnv(env, envErr)
}

// SessionRouteReader returns the daemon's route reader: a session of
// claudeAdapter (claudecode.AdapterName, passed in because claudecode
// imports this package) resolves its route through ObserveClaudeCodeRoute,
// every other adapter through the env-only ObserveRoute — Claude Code's
// settings files say nothing about another agent's route. granted reports
// the endpoint permission's CURRENT state and is read on every call.
func SessionRouteReader(claudeAdapter string, granted func() bool) func(pid int, adapter, cwd string) *session.RouteObservation {
	return func(pid int, adapter, cwd string) *session.RouteObservation {
		ok := granted != nil && granted()
		if adapter == claudeAdapter {
			return ObserveClaudeCodeRoute(pid, cwd, ok)
		}
		return ObserveRoute(pid, ok)
	}
}

// routeFromSettings turns a settings lookup into an observation, or nil when
// no settings layer sets the URL and the process env decides.
func routeFromSettings(r claudesettings.Result) *session.RouteObservation {
	if r.Status == claudesettings.StatusNotSet {
		return nil
	}
	obs := &session.RouteObservation{Source: "settings:" + r.Source.Layer, SettingsFile: r.Source.Path}
	switch {
	case r.Status != claudesettings.StatusFound:
		obs.Status = session.RouteUnreadable
	case r.Value == "":
		obs.Status = session.RouteAbsent
	default:
		endpoint, local, ok := redactEndpoint(r.Value)
		if !ok {
			obs.Status = session.RouteMalformed
			break
		}
		obs.Status, obs.Endpoint, obs.Local = session.RouteObserved, endpoint, local
	}
	return obs
}
