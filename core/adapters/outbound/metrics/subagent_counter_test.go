package metrics

import (
	"os"
	"path/filepath"
	"testing"

	"irrlicht/core/adapters/inbound/agents"
	"irrlicht/core/pkg/tailer"
)

// stateCountingParser is a minimal parser whose subagent count lives in its
// OWN state — the shape any adapter needs when the transcript reports child
// agents as start/stop events rather than as a number the tailer already
// tracks. GitHub Copilot is the first such adapter (#1256).
type stateCountingParser struct{ open int }

func (p *stateCountingParser) ParseLine(raw map[string]any) *tailer.ParsedEvent {
	switch raw["type"] {
	case "subagent.started":
		p.open++
	case "subagent.completed":
		if p.open > 0 {
			p.open--
		}
	}
	return &tailer.ParsedEvent{Skip: true}
}

// OpenSubagents reports the parser's OWN accumulated state, deliberately
// ignoring the passed metrics — the tailer has no field for it.
func (p *stateCountingParser) OpenSubagents(_ *tailer.SessionMetrics) int { return p.open }

// TestCountOpenSubagents_UsesTheParserThatActuallyParsed pins a seam defect
// found while assessing copilot's foreground-subagent cell (#1256).
//
// agents.SubagentCounters builds ONE parser at wiring time and closes over it,
// but the metrics Adapter constructs a SEPARATE parser per transcript path
// (parserFor, called from ComputeMetrics). The closed-over instance therefore
// never sees a single line, so any adapter whose count lives in parser state
// reports zero forever.
//
// Claude Code was immune purely by accident: its CountOpenSubagents is a pure
// function of the SessionMetrics passed in and ignores the receiver, so it
// never exercised the stale instance. Its own unit test also passes by
// construction, because it asserts on the same instance it fed.
func TestCountOpenSubagents_UsesTheParserThatActuallyParsed(t *testing.T) {
	dir := t.TempDir()
	transcript := filepath.Join(dir, "events.jsonl")
	// One subagent started and never completed → exactly one open child.
	lines := `{"type":"subagent.started","data":{"toolCallId":"call_a"}}` + "\n"
	if err := os.WriteFile(transcript, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}

	const adapterName = "state-counting"
	// Build the Registry exactly as production does: SubagentCounters closes
	// over its own freshly-made parser, independent of the one the Adapter
	// will create for this transcript.
	factories := map[string]agents.ParserFactory{
		adapterName: func() tailer.TranscriptParser { return &stateCountingParser{} },
	}
	counterParser := &stateCountingParser{}
	a := New(Registry{
		Parsers:      factories,
		FallbackName: adapterName,
		SubagentCounters: map[string]agents.SubagentCounter{
			adapterName: func(m *tailer.SessionMetrics) int {
				return counterParser.OpenSubagents(m)
			},
		},
	})

	got, err := a.ComputeMetrics(transcript, adapterName)
	if err != nil {
		t.Fatalf("ComputeMetrics: %v", err)
	}

	if got.OpenSubagents != 1 {
		t.Errorf("OpenSubagents = %d, want 1 — the count must come from the parser "+
			"instance that actually consumed the transcript, not from a separate "+
			"wiring-time instance that never saw a line", got.OpenSubagents)
	}
}
