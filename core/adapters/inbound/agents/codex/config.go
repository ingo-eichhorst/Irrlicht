package codex

import (
	"irrlicht/core/adapters/inbound/agents"
	"irrlicht/core/pkg/tailer"
)

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

// Config returns the registration record the daemon uses to wire this adapter.
func Config() agents.Config {
	return agents.Config{
		Name:         AdapterName,
		ProcessName:  ProcessName,
		RootDir:      rootDir,
		NewParser:    func() tailer.TranscriptParser { return &Parser{} },
		DiscoverPID:  DiscoverPID,
		DisplayName:  "Codex",
		IconSVGLight: iconSVGLight,
		IconSVGDark:  iconSVGDark,
	}
}
