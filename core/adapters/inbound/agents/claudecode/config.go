package claudecode

import (
	"irrlicht/core/adapters/inbound/agents"
	"irrlicht/core/pkg/tailer"
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

// Config returns the registration record the daemon uses to wire this adapter.
func Config() agents.Config {
	return agents.Config{
		Name:               AdapterName,
		ProcessName:        ProcessName,
		RootDir:            projectsDir,
		NewParser:          func() tailer.TranscriptParser { return &Parser{} },
		DiscoverPID:        DiscoverPID,
		CountOpenSubagents: CountOpenSubagents,
		DisplayName:        "Claude Code",
		IconSVGLight:       iconSVG,
		IconSVGDark:        iconSVG,
	}
}
