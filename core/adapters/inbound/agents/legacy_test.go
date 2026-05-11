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
)

// testAgents mirrors the production agentCfgs/agents slice used by main.go
// and replay.
func testAgents() []agent.Agent {
	return []agent.Agent{
		claudecode.Agent(),
		codex.Agent(),
		pi.Agent(),
		aider.Agent(),
		opencode.Agent(),
	}
}

func TestLegacyParsers_includesJSONLineParserAdapters(t *testing.T) {
	m := agents.LegacyParsers(testAgents())
	for _, name := range []string{claudecode.AdapterName, codex.AdapterName, pi.AdapterName} {
		if _, ok := m[name]; !ok {
			t.Errorf("LegacyParsers missing %q", name)
		}
	}
}

func TestLegacyParsers_omitsRawAndStoreAdapters(t *testing.T) {
	m := agents.LegacyParsers(testAgents())
	// aider (FilesUnderCWD/RawLineParser) and opencode (ProcessOwnedStore)
	// are intentionally absent — main.go and replay register them
	// explicitly via adapter-package imports.
	if _, ok := m[aider.AdapterName]; ok {
		t.Error("LegacyParsers should not include aider (FilesUnderCWD)")
	}
	if _, ok := m[opencode.AdapterName]; ok {
		t.Error("LegacyParsers should not include opencode (ProcessOwnedStore)")
	}
}

func TestLegacyPIDDiscoverers_coversAllAdapters(t *testing.T) {
	m := agents.LegacyPIDDiscoverers(testAgents())
	for _, name := range []string{
		claudecode.AdapterName, codex.AdapterName, pi.AdapterName,
		aider.AdapterName, opencode.AdapterName,
	} {
		if _, ok := m[name]; !ok {
			t.Errorf("LegacyPIDDiscoverers missing %q", name)
		}
	}
}

func TestLegacyProcessNames_matchesConfigShape(t *testing.T) {
	got := agents.LegacyProcessNames(testAgents())
	want := map[string]string{
		claudecode.AdapterName: "claude",
		codex.AdapterName:      "codex",
		pi.AdapterName:         "pi",
		aider.AdapterName:      "aider",    // CommandPattern fallback to Identity.Name
		opencode.AdapterName:   "opencode",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("LegacyProcessNames: got %v, want %v", got, want)
	}
}

func TestLegacySubagentCounters_onlyClaudecode(t *testing.T) {
	m := agents.LegacySubagentCounters(testAgents())
	if _, ok := m[claudecode.AdapterName]; !ok {
		t.Errorf("LegacySubagentCounters missing %q", claudecode.AdapterName)
	}
	for _, name := range []string{codex.AdapterName, pi.AdapterName, aider.AdapterName, opencode.AdapterName} {
		if _, ok := m[name]; ok {
			t.Errorf("LegacySubagentCounters should not include %q", name)
		}
	}
}

func TestLegacyMetricsProviders_onlyOpencode(t *testing.T) {
	m := agents.LegacyMetricsProviders(testAgents())
	if _, ok := m[opencode.AdapterName]; !ok {
		t.Errorf("LegacyMetricsProviders missing %q", opencode.AdapterName)
	}
	for _, name := range []string{claudecode.AdapterName, codex.AdapterName, pi.AdapterName, aider.AdapterName} {
		if _, ok := m[name]; ok {
			t.Errorf("LegacyMetricsProviders should not include %q", name)
		}
	}
}
