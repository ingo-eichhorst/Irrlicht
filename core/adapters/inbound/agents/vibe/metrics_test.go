package vibe

import (
	"os"
	"path/filepath"
	"testing"

	"irrlicht/core/pkg/tailer"
)

// TestTokenAccounting_EndToEnd drives a synthetic vibe transcript + meta.json
// sidecar through the PRODUCTION tailer and asserts the per-turn usage
// contributions accumulate into the session's cumulative token totals — the
// end-to-end path for assess cell 5.1 token-accounting. Cost stays 0 until
// mistral models carry pricing in core/pkg/capacity (unlock 1.8); this test
// pins the token half, which is model-price-independent.
func TestTokenAccounting_EndToEnd(t *testing.T) {
	dir := t.TempDir()
	transcript := filepath.Join(dir, transcriptFilename)
	lines := "" +
		`{"role":"user","content":"go","injected":false,"message_id":"u1"}` + "\n" +
		`{"role":"assistant","tool_calls":[{"id":"t1","function":{"name":"bash"}}],"message_id":"a1"}` + "\n" +
		`{"role":"tool","content":"ok","name":"bash","tool_call_id":"t1"}` + "\n" +
		`{"role":"assistant","content":"done","message_id":"a2"}` + "\n"
	if err := os.WriteFile(transcript, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	// The sidecar carries the session-cumulative token counts, as vibe writes
	// them after the final turn.
	meta := `{"environment":{"working_directory":"/tmp/p"},"config":{"active_model":"mistral-medium-3.5","auto_compact_threshold":200000},"stats":{"context_tokens":9000,"session_prompt_tokens":8500,"session_completion_tokens":320}}`
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}

	tl := tailer.NewTranscriptTailer(transcript, &Parser{}, AdapterName)
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatalf("TailAndProcess: %v", err)
	}

	if m.ModelName == "" {
		t.Errorf("ModelName empty, want the sidecar model")
	}
	if m.CumInputTokens != 8500 {
		t.Errorf("CumInputTokens = %d, want 8500", m.CumInputTokens)
	}
	if m.CumOutputTokens != 320 {
		t.Errorf("CumOutputTokens = %d, want 320", m.CumOutputTokens)
	}
	// The context-utilization bar resolves from the sidecar's auto-compaction
	// threshold (mistral models aren't in the capacity window map). No
	// config.models[] entries here, so this only pins the no-override
	// fallback-to-global path; TestContextWindow_PerModelAutoCompactThresholdOverride
	// below pins the per-model resolution (issue #1063).
	if m.ContextWindow != 200000 {
		t.Errorf("ContextWindow = %d, want 200000 (auto_compact_threshold)", m.ContextWindow)
	}
}

// TestContextWindow_PerModelAutoCompactThresholdOverride pins issue #1063:
// the EFFECTIVE auto-compaction threshold is the active model's own
// config.models[] override, not the top-level global default, whenever the
// two diverge. It also pins the alias-vs-name matching gotcha found in a
// live 2.19.1 meta.json: config.active_model ("mistral-medium-3.5") matches
// models[].alias, while models[].name ("mistral-vibe-cli-latest") is an
// unrelated CLI/product label that must NOT be used for the match — a decoy
// entry whose name equals active_model, but whose alias doesn't, is included
// below to prove the resolution isn't accidentally matching on name.
func TestContextWindow_PerModelAutoCompactThresholdOverride(t *testing.T) {
	dir := t.TempDir()
	transcript := filepath.Join(dir, transcriptFilename)
	lines := `{"role":"assistant","content":"done","message_id":"a1"}` + "\n"
	if err := os.WriteFile(transcript, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	meta := `{"environment":{"working_directory":"/tmp/p"},"config":{` +
		`"active_model":"mistral-medium-3.5","auto_compact_threshold":200000,` +
		`"models":[` +
		`{"name":"mistral-medium-3.5","alias":"decoy-name-match","auto_compact_threshold":1},` +
		`{"name":"mistral-vibe-cli-latest","alias":"mistral-medium-3.5","auto_compact_threshold":64000}` +
		`]},"stats":{"context_tokens":9000,"session_prompt_tokens":8500,"session_completion_tokens":320}}`
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}

	tl := tailer.NewTranscriptTailer(transcript, &Parser{}, AdapterName)
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatalf("TailAndProcess: %v", err)
	}

	if m.ContextWindow != 64000 {
		t.Errorf("ContextWindow = %d, want 64000 (active model's own models[].alias-matched override, not the 200000 global default or the 1 name-matched decoy)", m.ContextWindow)
	}
}

// TestTokenAccounting_ResumesAfterRotationReset pins issue #1063's primary
// fix: a rotation/truncation reset must not flatline vibe's token accounting
// at zero for the rest of the session.
//
// Vibe's meta.json session_prompt_tokens/session_completion_tokens are
// monotonic within a session, so the parser tracks its own high-water mark
// (lastPromptTokens/lastCompletionTokens) and emits only each turn's DELTA.
// Vibe 2.19.1's ACP /rewind path rewrites messages.jsonl IN PLACE
// (_overwrite_messages_sync with inplace=True) instead of forking a new
// session directory the way the TUI's /rewind does, so a rewind can land as
// a legitimate rotation under the tailer's watched root: the transcript
// shrinks (fileSize < lastOffset) and meta.json's cumulative counters drop
// to reflect the smaller, retained history.
//
// Before the fix: the tailer resets its OWN cumulative accumulators to 0 on
// rotation, but the parser's stale (larger) high-water mark survives. The
// next turn_done computes dPrompt/dCompletion against that stale mark, both
// go negative, both clamp to zero, and emitContribution's early return skips
// updating the high-water mark too — so every subsequent turn_done repeats
// the same clamp-to-zero comparison and CumInputTokens/CumOutputTokens never
// move off 0 again, even though real turns keep happening.
//
// After the fix: rotationResetter.ResetForRotation zeroes the parser's
// high-water mark alongside the tailer's own accumulators, so the very next
// turn_done's delta is computed against 0 and lands correctly.
func TestTokenAccounting_ResumesAfterRotationReset(t *testing.T) {
	dir := t.TempDir()
	transcript := filepath.Join(dir, transcriptFilename)
	metaPath := filepath.Join(dir, "meta.json")

	// Pre-rewind: one turn, generous token counts.
	preLines := `{"role":"assistant","content":"first turn done, plenty of padding text here so this line is longer than the post-rewind one","message_id":"a1"}` + "\n"
	if err := os.WriteFile(transcript, []byte(preLines), 0o644); err != nil {
		t.Fatal(err)
	}
	preMeta := `{"environment":{"working_directory":"/tmp/p"},"config":{"active_model":"mistral-medium-3.5","auto_compact_threshold":200000},"stats":{"context_tokens":9000,"session_prompt_tokens":8500,"session_completion_tokens":320}}`
	if err := os.WriteFile(metaPath, []byte(preMeta), 0o644); err != nil {
		t.Fatal(err)
	}

	tl := tailer.NewTranscriptTailer(transcript, &Parser{}, AdapterName)
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatalf("pre-rewind TailAndProcess: %v", err)
	}
	if m.CumInputTokens != 8500 || m.CumOutputTokens != 320 {
		t.Fatalf("pre-rewind tokens = (%d, %d), want (8500, 320)", m.CumInputTokens, m.CumOutputTokens)
	}

	// Simulate the ACP in-place /rewind: a strictly SMALLER transcript
	// (triggers the tailer's fileSize < lastOffset rotation detection) and a
	// meta.json whose cumulative counters reset lower, reflecting the
	// retained (rewound) history rather than continuing to grow from the
	// pre-rewind totals.
	postLines := `{"role":"assistant","content":"go","message_id":"a2"}` + "\n"
	if len(postLines) >= len(preLines) {
		t.Fatalf("test setup: post-rewind line (%d bytes) must be shorter than pre-rewind (%d bytes) to trigger rotation detection", len(postLines), len(preLines))
	}
	if err := os.WriteFile(transcript, []byte(postLines), 0o644); err != nil {
		t.Fatal(err)
	}
	postMeta := `{"environment":{"working_directory":"/tmp/p"},"config":{"active_model":"mistral-medium-3.5","auto_compact_threshold":200000},"stats":{"context_tokens":3100,"session_prompt_tokens":3000,"session_completion_tokens":100}}`
	if err := os.WriteFile(metaPath, []byte(postMeta), 0o644); err != nil {
		t.Fatal(err)
	}

	m, err = tl.TailAndProcess()
	if err != nil {
		t.Fatalf("post-rewind TailAndProcess: %v", err)
	}
	if m.CumInputTokens != 3000 {
		t.Errorf("post-rewind CumInputTokens = %d, want 3000 (flatlined at 0 means the parser's high-water mark wasn't reset)", m.CumInputTokens)
	}
	if m.CumOutputTokens != 100 {
		t.Errorf("post-rewind CumOutputTokens = %d, want 100 (flatlined at 0 means the parser's high-water mark wasn't reset)", m.CumOutputTokens)
	}
}
