package metrics

import (
	"os"
	"path/filepath"
	"testing"

	"irrlicht/core/adapters/inbound/agents"
	"irrlicht/core/domain/session"
	"irrlicht/core/pkg/tailer"
)

// usageLineParser turns every line into an assistant event, so the tailer
// yields metrics for any non-empty transcript.
type usageLineParser struct{}

func (usageLineParser) ParseLine(map[string]any) *tailer.ParsedEvent {
	return &tailer.ParsedEvent{EventType: "assistant"}
}

func writeUsageTranscript(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(p, []byte(`{"type":"assistant","timestamp":"2026-11-21T10:00:00Z"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestComputeMetrics_StampsTheSessionAdaptersOwnCoverage (#2061): the metrics
// adapter attaches the declaration registered under the session's adapter
// name — never the fallback adapter's, whose parser it borrows for an unknown
// name — and attaches a copy, so no session snapshot aliases the registry.
func TestComputeMetrics_StampsTheSessionAdaptersOwnCoverage(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // ledgers go under HOME
	declared := &session.UsageCoverage{
		TokenTypes:        []session.TokenType{session.TokenInput, session.TokenOutput},
		ExcludesSubagents: true,
	}
	a := New(Registry{
		Parsers:       map[string]agents.ParserFactory{"declared": func() tailer.TranscriptParser { return usageLineParser{} }},
		UsageCoverage: map[string]*session.UsageCoverage{"declared": declared},
		FallbackName:  "declared",
	})

	checkStampsACopyOfTheDeclaration(t, a, declared)
	checkUndeclaredAdapterGetsNoCoverage(t, a)
}

// checkStampsACopyOfTheDeclaration: a session of the declared adapter carries
// its declaration, as a copy.
func checkStampsACopyOfTheDeclaration(t *testing.T, a *Adapter, declared *session.UsageCoverage) {
	got, err := a.ComputeMetrics(writeUsageTranscript(t), "declared")
	if err != nil || got == nil {
		t.Fatalf("ComputeMetrics = (%v, %v), want metrics", got, err)
	}
	if !isTheDeclaredInputOutputCoverage(got.UsageCoverage) {
		t.Fatalf("UsageCoverage = %+v, want the declared one", got.UsageCoverage)
	}
	if got.UsageCoverage == declared {
		t.Error("the stamped coverage aliases the registry's declaration; it must be a copy")
	}
}

// isTheDeclaredInputOutputCoverage reports the test's declaration: output
// read, cache reads not, subagents excluded.
func isTheDeclaredInputOutputCoverage(c *session.UsageCoverage) bool {
	return c != nil && c.ExcludesSubagents && c.Observes(session.TokenOutput) && !c.Observes(session.TokenCacheRead)
}

// checkUndeclaredAdapterGetsNoCoverage: a session whose adapter declared
// nothing gets no coverage, though its parser is the fallback adapter's.
func checkUndeclaredAdapterGetsNoCoverage(t *testing.T, a *Adapter) {
	other, err := a.ComputeMetrics(writeUsageTranscript(t), "undeclared")
	if err != nil || other == nil {
		t.Fatalf("ComputeMetrics(undeclared) = (%v, %v), want metrics via the fallback parser", other, err)
	}
	if other.UsageCoverage != nil {
		t.Errorf("an undeclared adapter got coverage %+v — borrowing the fallback parser is not "+
			"evidence that the agent counts what the fallback adapter counts", other.UsageCoverage)
	}
}

// TestComputeMetrics_KeepsAProviderSetCoverage: a store-backed provider that
// declares its own coverage (opencode's 2.x path) is not overwritten by the
// adapter-level one.
func TestComputeMetrics_KeepsAProviderSetCoverage(t *testing.T) {
	own := &session.UsageCoverage{TokenTypes: session.TokenTypes()}
	a := New(Registry{
		MetricsProviders: map[string]agents.MetricsProvider{
			"store": func(string, string) (*session.SessionMetrics, error) {
				return &session.SessionMetrics{UsageCoverage: own}, nil
			},
			"plain": func(string, string) (*session.SessionMetrics, error) {
				return &session.SessionMetrics{}, nil
			},
		},
		UsageCoverage: map[string]*session.UsageCoverage{
			"store": {TokenTypes: []session.TokenType{session.TokenInput}},
			"plain": {TokenTypes: []session.TokenType{session.TokenOutput}},
		},
	})
	got, _ := a.ComputeMetrics("/db?session=1", "store")
	if got.UsageCoverage != own {
		t.Errorf("provider-set coverage replaced by %+v", got.UsageCoverage)
	}
	plain, _ := a.ComputeMetrics("/db?session=2", "plain")
	if !plain.UsageCoverage.Observes(session.TokenOutput) {
		t.Errorf("a provider path without its own coverage was not stamped: %+v", plain.UsageCoverage)
	}
}
