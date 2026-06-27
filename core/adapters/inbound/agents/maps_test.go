package agents_test

import (
	"reflect"
	"testing"

	"irrlicht/core/adapters/inbound/agents"
	"irrlicht/core/adapters/inbound/agents/aider"
	"irrlicht/core/adapters/inbound/agents/claudecode"
	"irrlicht/core/adapters/inbound/agents/codex"
	"irrlicht/core/adapters/inbound/agents/opencode"
	"irrlicht/core/adapters/inbound/agents/pi"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/backchannel"
)

// testAgents mirrors the production agents slice used by main.go and replay.
func testAgents() []agent.Agent {
	return []agent.Agent{
		claudecode.Agent(),
		codex.Agent(),
		pi.Agent(),
		aider.Agent(),
		opencode.Agent(),
	}
}

func TestParsers_includesJSONLineParserAdapters(t *testing.T) {
	m := agents.Parsers(testAgents())
	for _, name := range []string{claudecode.AdapterName, codex.AdapterName, pi.AdapterName} {
		if _, ok := m[name]; !ok {
			t.Errorf("Parsers missing %q", name)
		}
	}
}

func TestParsers_omitsRawAndStoreAdapters(t *testing.T) {
	m := agents.Parsers(testAgents())
	// aider (FilesUnderCWD/RawLineParser) and opencode (ProcessOwnedStore)
	// are intentionally absent — main.go and replay register them
	// explicitly via adapter-package imports.
	if _, ok := m[aider.AdapterName]; ok {
		t.Error("Parsers should not include aider (FilesUnderCWD)")
	}
	if _, ok := m[opencode.AdapterName]; ok {
		t.Error("Parsers should not include opencode (ProcessOwnedStore)")
	}
}

func TestPIDDiscoverers_coversAllAdapters(t *testing.T) {
	m := agents.PIDDiscoverers(testAgents())
	for _, name := range []string{
		claudecode.AdapterName, codex.AdapterName, pi.AdapterName,
		aider.AdapterName, opencode.AdapterName,
	} {
		if _, ok := m[name]; !ok {
			t.Errorf("PIDDiscoverers missing %q", name)
		}
	}
}

func TestProcessNames_matchesConfigShape(t *testing.T) {
	got := agents.ProcessNames(testAgents())
	want := map[string]string{
		claudecode.AdapterName: "claude",
		codex.AdapterName:      "codex",
		pi.AdapterName:         "pi",
		aider.AdapterName:      "aider", // CommandPattern fallback to Identity.Name
		opencode.AdapterName:   "opencode",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ProcessNames: got %v, want %v", got, want)
	}
}

func TestSubagentCounters_onlyClaudecode(t *testing.T) {
	m := agents.SubagentCounters(testAgents())
	if _, ok := m[claudecode.AdapterName]; !ok {
		t.Errorf("SubagentCounters missing %q", claudecode.AdapterName)
	}
	for _, name := range []string{codex.AdapterName, pi.AdapterName, aider.AdapterName, opencode.AdapterName} {
		if _, ok := m[name]; ok {
			t.Errorf("SubagentCounters should not include %q", name)
		}
	}
}

func TestControlPresets_onlyClaudecodeMapsCompact(t *testing.T) {
	m := agents.ControlPresets(testAgents())
	cc, ok := m[claudecode.AdapterName]
	if !ok {
		t.Fatalf("ControlPresets missing %q", claudecode.AdapterName)
	}
	if cc[backchannel.PresetCompact] != "/compact" {
		t.Errorf("claude-code compact preset = %q, want %q", cc[backchannel.PresetCompact], "/compact")
	}
	// Agents with no declared presets are absent so a preset rule degrades.
	for _, name := range []string{codex.AdapterName, pi.AdapterName, aider.AdapterName, opencode.AdapterName} {
		if _, ok := m[name]; ok {
			t.Errorf("ControlPresets should not include %q (no presets declared)", name)
		}
	}
}

func TestMetricsProviders_onlyOpencode(t *testing.T) {
	m := agents.MetricsProviders(testAgents())
	if _, ok := m[opencode.AdapterName]; !ok {
		t.Errorf("MetricsProviders missing %q", opencode.AdapterName)
	}
	for _, name := range []string{claudecode.AdapterName, codex.AdapterName, pi.AdapterName, aider.AdapterName} {
		if _, ok := m[name]; ok {
			t.Errorf("MetricsProviders should not include %q", name)
		}
	}
}
