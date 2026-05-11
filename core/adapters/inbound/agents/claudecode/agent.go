package claudecode

import (
	"irrlicht/core/domain/agent"
	"irrlicht/core/pkg/tailer"
)

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
