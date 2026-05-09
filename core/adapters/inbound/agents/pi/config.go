package pi

import (
	"irrlicht/core/adapters/inbound/agents"
	"irrlicht/core/pkg/tailer"
)

// Pi coding agent — Greek letter pi in a circle. Color picks contrast against
// the surrounding chrome: near-black on light themes, near-white on dark.
const iconSVGLight = `<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 100 100">
  <circle cx="50" cy="50" r="44" fill="none" stroke="#1A1A1A" stroke-width="8"/>
  <line x1="28" y1="30" x2="72" y2="30" stroke="#1A1A1A" stroke-width="8" stroke-linecap="round"/>
  <line x1="40" y1="30" x2="40" y2="74" stroke="#1A1A1A" stroke-width="8" stroke-linecap="round"/>
  <line x1="60" y1="30" x2="64" y2="74" stroke="#1A1A1A" stroke-width="8" stroke-linecap="round"/>
</svg>`

const iconSVGDark = `<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 100 100">
  <circle cx="50" cy="50" r="44" fill="none" stroke="#E0E0E0" stroke-width="8"/>
  <line x1="28" y1="30" x2="72" y2="30" stroke="#E0E0E0" stroke-width="8" stroke-linecap="round"/>
  <line x1="40" y1="30" x2="40" y2="74" stroke="#E0E0E0" stroke-width="8" stroke-linecap="round"/>
  <line x1="60" y1="30" x2="64" y2="74" stroke="#E0E0E0" stroke-width="8" stroke-linecap="round"/>
</svg>`

// Config returns the registration record the daemon uses to wire this adapter.
func Config() agents.Config {
	return agents.Config{
		Name:         AdapterName,
		ProcessName:  ProcessName,
		RootDir:      rootDir,
		NewParser:    func() tailer.TranscriptParser { return &Parser{} },
		DiscoverPID:  DiscoverPID,
		DisplayName:  "Pi",
		IconSVGLight: iconSVGLight,
		IconSVGDark:  iconSVGDark,
	}
}
