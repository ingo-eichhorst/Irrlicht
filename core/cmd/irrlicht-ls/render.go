package main

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"irrlicht/core/domain/session"
)

const (
	ansiReset     = "\033[0m"
	ansiGreen     = "\033[32m"
	ansiYellow    = "\033[33m"
	ansiRed       = "\033[31m"
	ansiBrightRed = "\033[91m"
)

// pressureColor maps a context pressure level to an ANSI color, mirroring
// the web dashboard's pressureColor (platforms/web/irrlicht.js).
func pressureColor(level string) string {
	switch level {
	case "critical":
		return ansiRed
	case "warning", "high":
		return ansiBrightRed
	case "caution", "medium":
		return ansiYellow
	default: // safe, unknown, empty
		return ansiGreen
	}
}

// colorize wraps s in the pressure level's ANSI color when useColor is set.
func colorize(s, level string, useColor bool) string {
	if !useColor {
		return s
	}
	return pressureColor(level) + s + ansiReset
}

// formatCostUSD mirrors the web dashboard's formatCost: "$X.XX", empty for
// non-positive values.
func formatCostUSD(usd float64) string {
	if usd <= 0 {
		return ""
	}
	return fmt.Sprintf("$%.2f", usd)
}

// projectName returns the display project, falling back to the CWD basename.
func projectName(s *session.SessionState) string {
	if s.ProjectName != "" {
		return s.ProjectName
	}
	return filepath.Base(s.CWD)
}

// adapterName returns the session's adapter, mapping the legacy empty value
// to "claude-code".
func adapterName(s *session.SessionState) string {
	if s.Adapter == "" {
		return "claude-code"
	}
	return s.Adapter
}

// sanitizeTerminal strips control characters (ANSI/OSC escapes, BEL, …)
// from transcript-derived text so agent output cannot inject terminal
// sequences into the user's terminal.
func sanitizeTerminal(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// renderGroups renders the dashboard group tree. Group headers are shown
// only when there is more than one group (web parity).
func renderGroups(w io.Writer, groups []*session.AgentGroup, useColor bool) {
	showHeaders := len(groups) > 1
	for i, g := range groups {
		if showHeaders {
			if i > 0 {
				fmt.Fprintln(w)
			}
			n := countAgents(g.Agents)
			noun := "sessions"
			if n == 1 {
				noun = "session"
			}
			fmt.Fprintf(w, "%s (%d %s)\n", g.Name, n, noun)
		}
		for _, a := range g.Agents {
			renderAgent(w, a, 0, useColor)
		}
	}
}

// countAgents counts agents including nested children.
func countAgents(agents []*session.Agent) int {
	n := 0
	for _, a := range agents {
		n += 1 + countAgents(a.Children)
	}
	return n
}

// renderAgent renders one session row, its detail lines (waiting question,
// task progress), then its children indented one level deeper.
func renderAgent(w io.Writer, a *session.Agent, depth int, useColor bool) {
	indent := strings.Repeat("  ", depth)
	fmt.Fprintf(w, "%s%s\n", indent, formatRow(a, useColor))

	detailIndent := indent + "    "
	if a.State == session.StateWaiting && a.Metrics != nil && a.Metrics.LastAssistantText != "" {
		text := sanitizeTerminal(strings.Join(strings.Fields(a.Metrics.LastAssistantText), " "))
		fmt.Fprintf(w, "%s? %s\n", detailIndent, text)
	}
	if a.Metrics != nil && len(a.Metrics.Tasks) > 0 {
		done := 0
		for _, t := range a.Metrics.Tasks {
			if t.Status == "completed" {
				done++
			}
		}
		fmt.Fprintf(w, "%s%d/%d completed\n", detailIndent, done, len(a.Metrics.Tasks))
	}

	for _, c := range a.Children {
		renderAgent(w, c, depth+1, useColor)
	}
}

// formatRow renders a single session line:
// state project id model ctx% Nk $cost adapter [N agents: Ww/Rr] (age ago)
func formatRow(a *session.Agent, useColor bool) string {
	s := a.SessionState
	age := time.Since(time.Unix(s.UpdatedAt, 0)).Truncate(time.Second)

	var sb strings.Builder
	fmt.Fprintf(&sb, "%-8s %-20s %s", s.State, projectName(s), shortID(s.SessionID))

	if m := s.Metrics; m != nil {
		if m.ModelName != "" {
			sb.WriteString(" " + m.ModelName)
		}
		if m.ContextWindow > 0 {
			pct := colorize(fmt.Sprintf("%.0f%%", m.ContextUtilization), m.PressureLevel, useColor)
			fmt.Fprintf(&sb, " %s %dk", pct, m.ContextWindow/1000)
		}
		if cost := formatCostUSD(m.EstimatedCostUSD); cost != "" {
			sb.WriteString(" " + cost)
		}
	}

	sb.WriteString(" " + adapterName(s))

	if sub := s.Subagents; sub != nil && sub.Total > 0 {
		noun := "agents"
		if sub.Total == 1 {
			noun = "agent"
		}
		fmt.Fprintf(&sb, " [%d %s: %dw/%dr]", sub.Total, noun, sub.Working, sub.Ready)
	}

	fmt.Fprintf(&sb, "  (%s ago)", age)
	return sb.String()
}
