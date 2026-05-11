package claudecode

import (
	"irrlicht/core/domain/agent"
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

// Agent returns the new declaration shape introduced in #159 Phase A.
// Mirrors Config() for legacy callers and will replace Config() once the
// daemon switches over (PR2/PR3). Parity tests in agent_parity_test.go
// assert Agent() and Config() produce equivalent data for every
// downstream-consumed field.
func Agent() agent.Agent {
	return agent.Agent{
		Identity: agent.Identity{
			Name:         AdapterName,
			DisplayName:  "Claude Code",
			IconSVGLight: iconSVG,
			IconSVGDark:  iconSVG,
		},
		Process: agent.Process{
			Match:         agent.ExactName{Name: ProcessName},
			PIDForSession: DiscoverPID,
		},
		Source: agent.FilesUnderRoot{
			Dir: projectsDir,
			Parser: agent.JSONLineParser{
				NewParser: func() agent.LineParser { return &Parser{} },
			},
		},
	}
}

// OpenSubagents lets the new metrics-collector path (PR3+) discover the
// subagent counter via type assertion on the LineParser returned by
// JSONLineParser.NewParser. Delegates to the existing CountOpenSubagents
// free function so the count produced is identical to what the legacy
// agents.Config.CountOpenSubagents pipeline produced.
func (p *Parser) OpenSubagents(m *tailer.SessionMetrics) int {
	return CountOpenSubagents(m)
}
