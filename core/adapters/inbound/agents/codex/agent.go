package codex

import "irrlicht/core/domain/agent"

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
