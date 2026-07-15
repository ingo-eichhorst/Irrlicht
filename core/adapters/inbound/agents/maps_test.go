package agents_test

import (
	"reflect"
	"sort"
	"testing"
	"time"

	"irrlicht/core/adapters/inbound/agents"
	"irrlicht/core/adapters/inbound/agents/aider"
	"irrlicht/core/adapters/inbound/agents/antigravity"
	"irrlicht/core/adapters/inbound/agents/claudecode"
	"irrlicht/core/adapters/inbound/agents/codex"
	"irrlicht/core/adapters/inbound/agents/geminicli"
	"irrlicht/core/adapters/inbound/agents/kirocli"
	"irrlicht/core/adapters/inbound/agents/opencode"
	"irrlicht/core/adapters/inbound/agents/pi"
	"irrlicht/core/adapters/inbound/agents/vibe"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/backchannel"
	"irrlicht/core/pkg/tailer"
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

// --- Census tests over the REAL agents.All() ---
//
// The tests above deliberately use the hand-rolled testAgents() to exercise
// the maps.go builders against a fixed, minimal input. The censuses below
// instead run over agents.All() — the production adapter slice — because
// their whole purpose is to notice when a NEW adapter (or a new seam
// implementation) changes the shape of the monitoring surface.
//
// They exist because .claude/skills/ir:agent-releases/references/
// monitoring-surface.md used to assert these facts in prose ("opencode is
// the only ProcessOwnedStore adapter", the 9-row parser-seam table). Prose
// drifts silently; issue #1096 converted the mechanically-checkable claims
// into invariants. When one of these fails, the fix is usually to update the
// literal below AND the doc — the failure is the reminder to do both.

// TestSourceCensus pins which adapters use each agent.Source variant.
// agent.Source is a sealed sum (unexported isSource marker), so this
// type-switch is exhaustive by construction: a brand-new variant lands in
// the default arm and fails, mirroring the panic in
// core/cmd/irrlichd/wiring.go's buildAgentWatchers.
func TestSourceCensus(t *testing.T) {
	want := map[string][]string{
		"FilesUnderRoot": {
			claudecode.AdapterName, codex.AdapterName, pi.AdapterName,
			kirocli.AdapterName, geminicli.AdapterName,
			antigravity.AdapterName, vibe.AdapterName,
		},
		"FilesUnderCWD":     {aider.AdapterName},
		"ProcessOwnedStore": {opencode.AdapterName},
	}

	got := map[string][]string{}
	for _, a := range agents.All() {
		name := a.Identity.Name
		switch a.Source.(type) {
		case agent.FilesUnderRoot:
			got["FilesUnderRoot"] = append(got["FilesUnderRoot"], name)
		case agent.FilesUnderCWD:
			got["FilesUnderCWD"] = append(got["FilesUnderCWD"], name)
		case agent.ProcessOwnedStore:
			got["ProcessOwnedStore"] = append(got["ProcessOwnedStore"], name)
		default:
			t.Errorf("adapter %q has unhandled agent.Source variant %T — add a case here, "+
				"wire its watcher in buildAgentWatchers, and document it in monitoring-surface.md",
				name, a.Source)
		}
	}

	assertNameSets(t, "agent.Source variant", want, got)
}

// TestParserSeamCensus pins which adapters implement each optional parser
// seam — the "Optional parser seams — who implements what" table in
// monitoring-surface.md. Each seam is consumed by the tailer (or the replay
// engine) via a type assertion, so implementing one silently changes
// runtime behavior; this test makes that visible.
//
// Four of the seams are unexported in package tailer and cannot be named
// from this external test package. They are mirrored below as structurally
// identical local interfaces — Go satisfies interfaces structurally, so a
// mirror matches exactly what the tailer asserts. The mirrors must be kept
// in sync with core/pkg/tailer/parser.go by hand: if a seam's method
// signature changes there, update the mirror here too (a stale mirror makes
// this test assert the wrong contract rather than fail loudly).
func TestParserSeamCensus(t *testing.T) {
	type (
		// mirrors tailer.idleFlusher (parser.go)
		idleFlusher interface {
			IdleFlush(idleFor time.Duration) *tailer.ParsedEvent
		}
		// mirrors tailer.queuedTurnSplitter (parser.go)
		queuedTurnSplitter interface{ SplitsQueuedFollowUpTurns() bool }
		// mirrors tailer.rotationResetter (parser.go)
		rotationResetter interface{ ResetForRotation() }
	)

	seams := []struct {
		seam       string
		implements func(any) bool
		want       []string
	}{
		// tailer.RawLineParser (exported) — the interface, not the
		// agent.RawLineParser FileParser struct of the same name.
		{"RawLineParser", func(p any) bool { _, ok := p.(tailer.RawLineParser); return ok },
			[]string{aider.AdapterName}},
		{"idleFlusher", func(p any) bool { _, ok := p.(idleFlusher); return ok },
			[]string{aider.AdapterName}},
		{"queuedTurnSplitter", func(p any) bool { _, ok := p.(queuedTurnSplitter); return ok },
			[]string{vibe.AdapterName}},
		{"rotationResetter (ResetForRotation)", func(p any) bool { _, ok := p.(rotationResetter); return ok },
			[]string{vibe.AdapterName}},
		// agent.PendingContributor is the exported public twin of
		// tailer.pendingContributor, kept in sync at file_parser.go.
		{"pendingContributor", func(p any) bool { _, ok := p.(agent.PendingContributor); return ok },
			[]string{claudecode.AdapterName}},
		{"ParserStateProvider", func(p any) bool { _, ok := p.(tailer.ParserStateProvider); return ok },
			[]string{claudecode.AdapterName, codex.AdapterName}},
		{"TranscriptPathAware", func(p any) bool { _, ok := p.(tailer.TranscriptPathAware); return ok },
			[]string{kirocli.AdapterName, vibe.AdapterName, antigravity.AdapterName}},
		{"ReplayStoreStager", func(p any) bool { _, ok := p.(tailer.ReplayStoreStager); return ok },
			[]string{antigravity.AdapterName}},
	}

	all := agents.All()
	for _, s := range seams {
		t.Run(s.seam, func(t *testing.T) {
			var got []string
			for _, a := range all {
				p := parserOf(t, a)
				if p == nil {
					continue // ProcessOwnedStore carries no parser factory
				}
				if s.implements(p) {
					got = append(got, a.Identity.Name)
				}
			}
			assertNameSets(t, s.seam+" implementers",
				map[string][]string{s.seam: s.want}, map[string][]string{s.seam: got})
		})
	}
}

// parserOf returns a fresh parser instance for a's Source, or nil when the
// variant carries no parser factory (ProcessOwnedStore reads a store
// instead). Fails the test on an unhandled variant so a new sum member
// can't slip past the seam census by silently yielding nil.
func parserOf(t *testing.T, a agent.Agent) any {
	t.Helper()
	switch s := a.Source.(type) {
	case agent.FilesUnderRoot:
		switch p := s.Parser.(type) {
		case agent.JSONLineParser:
			return p.NewParser()
		case agent.RawLineParser:
			return p.NewParser()
		default:
			t.Errorf("adapter %q: unhandled agent.FileParser variant %T", a.Identity.Name, s.Parser)
			return nil
		}
	case agent.FilesUnderCWD:
		return s.Parser.NewParser()
	case agent.ProcessOwnedStore:
		return nil
	default:
		t.Errorf("adapter %q: unhandled agent.Source variant %T", a.Identity.Name, a.Source)
		return nil
	}
}

// assertNameSets compares want/got adapter-name sets per key, order-
// insensitively, reporting each key's difference on its own line.
func assertNameSets(t *testing.T, subject string, want, got map[string][]string) {
	t.Helper()
	keys := map[string]bool{}
	for k := range want {
		keys[k] = true
	}
	for k := range got {
		keys[k] = true
	}
	for k := range keys {
		w, g := append([]string(nil), want[k]...), append([]string(nil), got[k]...)
		sort.Strings(w)
		sort.Strings(g)
		if !reflect.DeepEqual(w, g) {
			t.Errorf("%s %q: got %v, want %v", subject, k, g, w)
		}
	}
}
