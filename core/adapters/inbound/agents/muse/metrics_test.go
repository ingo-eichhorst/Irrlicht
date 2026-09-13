package muse

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"irrlicht/core/pkg/tailer"
)

// compactJSONLines collapses each multi-line backtick literal in lines onto
// one physical line: a JSONL file needs exactly one JSON object per file
// line, but a readable multi-line Go raw string literal embeds a real
// newline (plus source indentation) in the middle of the JSON. JSON permits
// insignificant whitespace between tokens, and none of these fixtures'
// string values depend on exact whitespace, so normalizing every run of
// whitespace (including the embedded newlines) to a single space is safe.
func compactJSONLines(lines []string) string {
	compacted := make([]string, len(lines))
	for i, l := range lines {
		compacted[i] = strings.Join(strings.Fields(l), " ")
	}
	return strings.Join(compacted, "\n") + "\n"
}

// TestTokenAccounting_EndToEnd drives a synthetic two-turn Muse transcript
// through the PRODUCTION tailer (the vibe/junie TestTokenAccounting_EndToEnd
// shape) and asserts the per-call model_completed contributions accumulate
// into the session's cumulative token totals, that the goal_usage_attribution
// duplicate of the SAME usage does NOT inflate the total a second time, and
// that model identity survives across turns.
func TestTokenAccounting_EndToEnd(t *testing.T) {
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "2026", "09", "13", "01a09c40-6dd8-7732-8c3f-5a7618ffaee4")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(sessionDir, transcriptFilename)

	lines := []string{
		// Session opens; model resolves for the first run.
		`{"payload_type":"run.model.configured","payload":{"kind":"run_model","record":{
			"provider_id":"meta","model_id":"muse-spark-1.3-contributor","source":"startup"}}}`,
		`{"payload_type":"runtime.session","payload":{"kind":"run","run_id":"r1",
			"event":{"kind":"started","prompt":"ls"}}}`,
		// Turn 1's LLM call: 35023 input (29297 of it cache-read), 378 output.
		`{"payload_type":"runtime.session","payload":{"kind":"run","run_id":"r1",
			"event":{"kind":"model_completed","usage":{"input_tokens":35023,"output_tokens":378,
			"cached_tokens":29297,"cache_write_tokens":0,"cache_read_tokens":29297,
			"reasoning_tokens":76},"duration_ms":4343,"model":"muse-spark-1.3-contributor"}}}`,
		// The SAME usage re-emitted with ownership metadata — must not double-count.
		`{"payload_type":"runtime.session","payload":{"kind":"run","run_id":"r1",
			"event":{"kind":"goal_usage_attribution","record":{"usage_id":"u1","usage_family":"provider",
			"quantity":{"unit":"tokens","input_tokens":35023,"output_tokens":378,"cached_tokens":29297,
			"reasoning_tokens":76,"main_llm_steps":1},
			"owner":{"requester_kind":"main","owner_type":"main_root"}}}}}`,
		`{"payload_type":"runtime.session","payload":{"kind":"run","run_id":"r1",
			"event":{"kind":"terminal","terminal":"completed","reason":null,"turn_duration_ms":14542}}}`,
		// Turn 2: a second, smaller LLM call, same model.
		`{"payload_type":"runtime.session","payload":{"kind":"run","run_id":"r2",
			"event":{"kind":"started","prompt":"go on"}}}`,
		`{"payload_type":"runtime.session","payload":{"kind":"run","run_id":"r2",
			"event":{"kind":"model_completed","usage":{"input_tokens":1000,"output_tokens":50,
			"cached_tokens":0,"cache_write_tokens":0,"cache_read_tokens":0,"reasoning_tokens":0},
			"duration_ms":900,"model":"muse-spark-1.3-contributor"}}}`,
		`{"payload_type":"runtime.session","payload":{"kind":"run","run_id":"r2",
			"event":{"kind":"terminal","terminal":"completed","reason":null,"turn_duration_ms":900}}}`,
	}
	if err := os.WriteFile(transcript, []byte(compactJSONLines(lines)), 0o644); err != nil {
		t.Fatal(err)
	}

	m, err := tailer.NewTranscriptTailer(transcript, &Parser{}, AdapterName).TailAndProcess()
	if err != nil {
		t.Fatalf("TailAndProcess: %v", err)
	}

	if m.ModelName != "muse-spark-1.3-contributor" {
		t.Errorf("ModelName = %q, want muse-spark-1.3-contributor", m.ModelName)
	}

	// Turn 1 nets 35023-29297=5726 fresh input + 29297 cache-read; turn 2
	// adds 1000 fresh input with no cache. goal_usage_attribution's
	// duplicate of turn 1's usage must contribute NOTHING.
	wantInput := (35023 - 29297) + 1000
	wantOutput := int64(378 + 50)
	wantCacheRead := int64(29297)
	if m.CumInputTokens != int64(wantInput) {
		t.Errorf("CumInputTokens = %d, want %d (would be higher if goal_usage_attribution also counted)", m.CumInputTokens, wantInput)
	}
	if m.CumOutputTokens != wantOutput {
		t.Errorf("CumOutputTokens = %d, want %d", m.CumOutputTokens, wantOutput)
	}
	if m.CumCacheReadTokens != wantCacheRead {
		t.Errorf("CumCacheReadTokens = %d, want %d", m.CumCacheReadTokens, wantCacheRead)
	}
	if m.LastEventType != "turn_done" {
		t.Errorf("LastEventType = %q, want turn_done", m.LastEventType)
	}
	if m.HasOpenToolCall {
		t.Error("expected no open tool call")
	}
	if m.SessionError != nil {
		t.Errorf("SessionError = %+v, want nil", m.SessionError)
	}
	// No pricing entry exists for "muse-spark-1.3-contributor" in
	// core/pkg/capacity today (confirmed: TailAndProcess logs "no pricing for
	// model" when this test runs), so cost is legitimately 0 — the same
	// documented gap vibe's own TestTokenAccounting_EndToEnd carries for
	// Mistral models. This test pins the TOKEN half, which is
	// model-price-independent.
	if m.EstimatedCostUSD != 0 {
		t.Errorf("EstimatedCostUSD = %v, want 0 (no pricing catalog entry for this model yet)", m.EstimatedCostUSD)
	}
}

// TestSessionError_ClearsOnNextTurn drives a failed run followed by a
// successful one through the production tailer and pins the interaction this
// parser relies on but does not itself implement (core/pkg/tailer's
// clearSessionErrorOnRecovery): ParsedEvent.StartsNewUserTurn (raised by the
// second run's "started": ClearToolNames with no ToolResultIDs) clears ANY
// standing error unconditionally, regardless of the phase parseRunTerminal
// chose. This is what makes ErrorPhaseUnknown a safe, non-sticky choice for
// muse's failed-run SessionError: the very next run's own "started" always
// clears it before a bare turn_done check would ever be reached.
func TestSessionError_ClearsOnNextTurn(t *testing.T) {
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "2026", "09", "13", "01a09c40-6dd8-7732-8c3f-5a7618ffaee4")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(sessionDir, transcriptFilename)

	lines := []string{
		`{"payload_type":"runtime.session","payload":{"kind":"run","run_id":"r1",
			"event":{"kind":"started","prompt":"do the thing"}}}`,
		`{"payload_type":"runtime.session","payload":{"kind":"run","run_id":"r1",
			"event":{"kind":"run_fatal_error_classified","error_class":"config_error"}}}`,
		`{"payload_type":"runtime.session","payload":{"kind":"run","run_id":"r1",
			"event":{"kind":"terminal","terminal":"failed",
			"reason":"invalid run configuration: provider does not support base instructions",
			"turn_duration_ms":11}}}`,
	}
	if err := os.WriteFile(transcript, []byte(compactJSONLines(lines)), 0o644); err != nil {
		t.Fatal(err)
	}
	tl := tailer.NewTranscriptTailer(transcript, &Parser{}, AdapterName)
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatalf("TailAndProcess: %v", err)
	}
	if m.SessionError == nil {
		t.Fatal("expected a sticky SessionError after the failed run")
	}

	// A second, successful run starts and completes.
	more := []string{
		`{"payload_type":"runtime.session","payload":{"kind":"run","run_id":"r2",
			"event":{"kind":"started","prompt":"try again"}}}`,
		`{"payload_type":"runtime.session","payload":{"kind":"run","run_id":"r2",
			"event":{"kind":"terminal","terminal":"completed","reason":null,"turn_duration_ms":100}}}`,
	}
	f, err := os.OpenFile(transcript, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(compactJSONLines(more)); err != nil {
		t.Fatal(err)
	}
	f.Close()

	m, err = tl.TailAndProcess()
	if err != nil {
		t.Fatalf("TailAndProcess (second pass): %v", err)
	}
	if m.SessionError != nil {
		t.Errorf("SessionError = %+v, want nil — the next run's own \"started\" clears any standing error", m.SessionError)
	}
}
