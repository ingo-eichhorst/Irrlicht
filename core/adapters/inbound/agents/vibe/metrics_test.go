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
	meta := `{"environment":{"working_directory":"/tmp/p"},"config":{"active_model":"mistral-medium-3.5"},"stats":{"context_tokens":9000,"session_prompt_tokens":8500,"session_completion_tokens":320}}`
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
}
