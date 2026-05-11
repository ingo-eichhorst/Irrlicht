package codex

import "irrlicht/core/domain/agent"

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

// Agent returns the new declaration shape introduced in #159 Phase A.
// Mirrors Config() for legacy callers and will replace Config() once the
// daemon switches over (PR2/PR3). Parity tests assert equivalence.
func Agent() agent.Agent {
	return agent.Agent{
		Identity: agent.Identity{
			Name:         AdapterName,
			DisplayName:  "Codex",
			IconSVGLight: iconSVGLight,
			IconSVGDark:  iconSVGDark,
		},
		Process: agent.Process{
			Match:         agent.ExactName{Name: ProcessName},
			PIDForSession: DiscoverPID,
		},
		Source: agent.FilesUnderRoot{
			Dir: rootDir,
			Parser: agent.JSONLineParser{
				NewParser: func() agent.LineParser { return &Parser{} },
			},
		},
	}
}
