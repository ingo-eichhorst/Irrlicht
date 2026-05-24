package agentwiring

import (
	"testing"

	"irrlicht/core/adapters/inbound/agents"
	"irrlicht/core/adapters/inbound/agents/aider"
	"irrlicht/core/adapters/inbound/agents/opencode"
)

// TestParserFactories_coversEveryAdapter is the regression guard for
// issue #461 finding #3: the daemon and the viewer must build their
// metrics collector from one shared parser map, so a new adapter can
// never be wired in one and silently dropped in the other. If a future
// FilesUnderCWD/ProcessOwnedStore adapter is added to agents.All() but
// not given an override here, this fails instead of failing silently at
// runtime in the viewer.
func TestParserFactories_coversEveryAdapter(t *testing.T) {
	all := agents.All()
	factories := ParserFactories(all)

	for _, a := range all {
		name := a.Identity.Name
		f, ok := factories[name]
		if !ok {
			t.Errorf("ParserFactories is missing adapter %q — it would fall back silently", name)
			continue
		}
		if f == nil || f() == nil {
			t.Errorf("ParserFactories[%q] produced a nil parser", name)
		}
	}
}

// TestParserFactories_addsOverridesAgentsParsersOmits documents *why*
// the shared helper exists: agents.Parsers omits the non-JSONL adapters,
// and the overrides put them back. If agents.Parsers ever starts
// including them, this test flags the now-redundant override.
func TestParserFactories_addsOverridesAgentsParsersOmits(t *testing.T) {
	all := agents.All()

	base := agents.Parsers(all)
	if _, ok := base[aider.AdapterName]; ok {
		t.Errorf("agents.Parsers unexpectedly includes %q; the override may be redundant", aider.AdapterName)
	}
	if _, ok := base[opencode.AdapterName]; ok {
		t.Errorf("agents.Parsers unexpectedly includes %q; the override may be redundant", opencode.AdapterName)
	}

	full := ParserFactories(all)
	if _, ok := full[aider.AdapterName]; !ok {
		t.Errorf("ParserFactories must add the %q override", aider.AdapterName)
	}
	if _, ok := full[opencode.AdapterName]; !ok {
		t.Errorf("ParserFactories must add the %q override", opencode.AdapterName)
	}
}

// TestBuildMetricsCollector_returnsUsableCollector is a thin smoke test
// that the constructor wires a non-nil collector that satisfies the port.
func TestBuildMetricsCollector_returnsUsableCollector(t *testing.T) {
	c := BuildMetricsCollector(agents.All())
	if c == nil {
		t.Fatal("BuildMetricsCollector returned nil")
	}
	// Empty transcript path is the documented (nil, nil) no-data case;
	// exercising it proves the collector is callable through the port.
	m, err := c.ComputeMetrics("", "claude-code")
	if err != nil {
		t.Fatalf("ComputeMetrics on empty path returned error: %v", err)
	}
	if m != nil {
		t.Fatalf("expected nil metrics for empty transcript path, got %+v", m)
	}
}
