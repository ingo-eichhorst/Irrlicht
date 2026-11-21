package processlifecycle

import (
	"os"

	"irrlicht/core/adapters/inbound/agents/claudecode/settingsenv"
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
	return settingsHost{Home: home, ManagedDir: settingsenv.ManagedDir(), MDMProfiles: settingsenv.MDMProfiles()}
}

// ObserveClaudeCodeRoute — RED-FIRST SCAFFOLD: delegates to main's env-only
// ObserveRoute unchanged, so the #2010 cases A and B run against main's
// behaviour before the fix exists.
func ObserveClaudeCodeRoute(pid int, cwd string, granted bool) *session.RouteObservation {
	_ = cwd
	return ObserveRoute(pid, granted)
}
