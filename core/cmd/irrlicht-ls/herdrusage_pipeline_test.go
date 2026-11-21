package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"irrlicht/core/adapters/inbound/agents"
	"irrlicht/core/adapters/outbound/metrics"
	"irrlicht/core/domain/session"
	"irrlicht/core/pkg/tailer"
)

// scriptedUsageParser turns each transcript line into one billable turn:
// {"model": …, "in": …, "out": …, "cost": …}, cost optional.
type scriptedUsageParser struct{}

func (scriptedUsageParser) ParseLine(raw map[string]any) *tailer.ParsedEvent {
	num := func(k string) int64 { v, _ := raw[k].(float64); return int64(v) }
	model, _ := raw["model"].(string)
	c := &tailer.PerTurnContribution{Model: model, Usage: tailer.UsageBreakdown{Input: num("in"), Output: num("out")}}
	if v, ok := raw["cost"].(float64); ok {
		c.ProviderCostUSD = &v
	}
	return &tailer.ParsedEvent{EventType: "assistant", Timestamp: tailer.ParseTimestamp(raw), ModelName: model, Contribution: c}
}

// TestHerdrUsage_PipelineKeepsUnknownsNull runs the whole daemon-side path a
// session's usage takes before this mode reads it — tailer, the metrics
// adapter's tailer→domain conversion and coverage stamp, MergeMetrics, the
// session JSON the daemon serves — and then this mode, for #2061's two "zero
// that means unknown" cases. The goldens above start from a JSON fixture and
// cannot see a provenance field dropped on the way; this test can, and it is
// the target of tools/lib/herdr-usage-mutations_test.sh.
func TestHerdrUsage_PipelineKeepsUnknownsNull(t *testing.T) {
	p := newUsagePipeline(t)
	t.Run("a priced session shows its cost and names the table", p.checkPricedSession)
	t.Run("an unpriced model makes the cost null, not the priced part", p.checkUnpricedModel)
	t.Run("a provider-cost turn makes the tokens null, keeps its cost", p.checkProviderCostTurn)
}

// usagePipeline is the daemon-side path under test: a metrics adapter over
// scriptedUsageParser, pricing from a dated one-model table, its transcripts
// and ledgers under home.
type usagePipeline struct {
	home      string
	collector *metrics.Adapter
}

func newUsagePipeline(t *testing.T) *usagePipeline {
	home := t.TempDir()
	t.Setenv("HOME", home) // the metrics adapter's ledgers live under HOME
	// A dated one-model price table. capacity.DefaultCapacityManager reads it
	// once per process, so nothing else in this package may price first.
	priceTable := filepath.Join(home, "prices.json")
	if err := os.WriteFile(priceTable, []byte(`{"fetched_at":"2026-11-20T00:00:00Z","config":{"version":"remote-v2",
		"last_updated":"2026-11-20","models":{"claude-sonnet-4-5":{"context_window":200000,
		"pricing":{"input_per_mtok":3,"output_per_mtok":15,"cache_read_per_mtok":0.3,"cache_creation_per_mtok":3.75}}}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("IRRLICHT_MODEL_CAPACITY_CACHE", priceTable)

	collector := metrics.New(metrics.Registry{
		Parsers: map[string]agents.ParserFactory{"scripted": func() tailer.TranscriptParser { return scriptedUsageParser{} }},
		UsageCoverage: map[string]*session.UsageCoverage{"scripted": {
			TokenTypes: session.TokenTypes(), ExcludesSubagents: true,
		}},
		FallbackName: "scripted",
	})
	return &usagePipeline{home: home, collector: collector}
}

// answer runs the transcript lines through the pipeline as session name, in
// pane w1:p1, and returns the usage document's one session.
func (p *usagePipeline) answer(t *testing.T, name string, lines ...string) map[string]any {
	t.Helper()
	state := &session.SessionState{
		SessionID: name, State: session.StateWorking, Adapter: "scripted",
		Launcher: &session.Launcher{HerdrPaneID: "w1:p1"},
		Metrics:  session.MergeMetrics(p.computeMetrics(t, name, lines), &session.SessionMetrics{LastEventType: "user"}),
	}
	served, err := json.Marshal(map[string]any{"groups": session.BuildDashboard([]*session.SessionState{state})})
	if err != nil {
		t.Fatal(err)
	}
	return answerServed(t, served)
}

// computeMetrics writes the lines as a transcript and runs the metrics
// adapter over it.
func (p *usagePipeline) computeMetrics(t *testing.T, name string, lines []string) *session.SessionMetrics {
	t.Helper()
	path := filepath.Join(p.home, name+".jsonl")
	body := ""
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	fresh, err := p.collector.ComputeMetrics(path, "scripted")
	if err != nil || fresh == nil {
		t.Fatalf("ComputeMetrics = (%v, %v)", fresh, err)
	}
	return fresh
}

// answerServed serves a /api/v1/sessions body, runs this mode for pane w1:p1
// against it, and returns the document's one session.
func answerServed(t *testing.T, served []byte) map[string]any {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(served) }))
	defer srv.Close()
	code, stdout, stderr := runUsage(usageEnv(srv.URL, "w1:p1", ""))
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	var doc struct {
		Sessions []map[string]any `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil || len(doc.Sessions) != 1 {
		t.Fatalf("document = %s (%v), want one session", stdout, err)
	}
	return doc.Sessions[0]
}

// pipelineField reads a dotted path from one document session; a missing
// path reads as nil.
func pipelineField(s map[string]any, path string) any {
	v, _ := lookupDotted(s, path)
	return v
}

func (p *usagePipeline) checkPricedSession(t *testing.T) {
	s := p.answer(t, "priced", `{"timestamp":"2026-11-21T10:00:00Z","model":"claude-sonnet-4-5","in":1000000,"out":0}`)
	if got := pipelineField(s, "cost.usd"); got != 3.0 {
		t.Errorf("cost.usd = %v, want 3 (1M input tokens at $3/MTok)", got)
	}
	if got := pipelineField(s, "cost.pricing_source"); got != "litellm@2026-11-20" {
		t.Errorf("cost.pricing_source = %v, want litellm@2026-11-20", got)
	}
	if got := pipelineField(s, "tokens.input"); got != 1000000.0 {
		t.Errorf("tokens.input = %v, want 1000000", got)
	}
}

func (p *usagePipeline) checkUnpricedModel(t *testing.T) {
	s := p.answer(t, "unpriced",
		`{"timestamp":"2026-11-21T10:00:00Z","model":"claude-sonnet-4-5","in":1000000,"out":0}`,
		`{"timestamp":"2026-11-21T10:00:05Z","model":"local-unpriced","in":50,"out":5}`)
	if got := pipelineField(s, "cost.usd"); got != nil {
		t.Errorf("cost.usd = %v, want null: local-unpriced's tokens were never priced", got)
	}
	if reason, _ := s["unavailable"].(map[string]any)["cost.usd"].(string); !strings.Contains(reason, "local-unpriced") {
		t.Errorf("unavailable[cost.usd] = %q, want it to name local-unpriced", reason)
	}
}

func (p *usagePipeline) checkProviderCostTurn(t *testing.T) {
	s := p.answer(t, "provider",
		`{"timestamp":"2026-11-21T10:00:00Z","model":"claude-sonnet-4-5","in":900,"out":90,"cost":0.5}`)
	for _, f := range []string{"input", "output", "cache_read", "cache_write"} {
		if got := pipelineField(s, "tokens."+f); got != nil {
			t.Errorf("tokens.%s = %v, want null: the turn's tokens were dropped for its provider cost", f, got)
		}
	}
	if got := pipelineField(s, "cost.usd"); got != 0.5 {
		t.Errorf("cost.usd = %v, want the provider's 0.5", got)
	}
	if got := pipelineField(s, "cost.pricing_source"); got != session.PricingSourceProviderReported {
		t.Errorf("cost.pricing_source = %v, want %s", got, session.PricingSourceProviderReported)
	}
}
