package kirocli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// liveSidecar mirrors the live 2.6.0 sidecar shape (session
// 0217167a-2112-48b1-a745-83232b028211, #599), trimmed: rts_model_state is
// verbatim; conversation_metadata stands in for the potentially-huge blob the
// walker must skip without decoding.
const liveSidecar = `{
  "created_at": "2026-06-05T07:12:18.331439Z",
  "cwd": "/Users/test/project",
  "session_created_reason": "UserInitiated",
  "session_id": "0217167a-2112-48b1-a745-83232b028211",
  "session_state": {
    "agent_name": "kiro_default",
    "conversation_metadata": {"user_turn_metadatas": [{"metering_usage": [{"value": 0.057, "unit": "credit"}], "context_usage_percentage": 1.76, "input_token_count": 0, "output_token_count": 0}]},
    "permissions": {"trust_all": true},
    "rts_model_state": {"conversation_id": "0217167a-2112-48b1-a745-83232b028211", "model_info": {"model_id": "auto", "context_window_tokens": 200000}, "context_usage_percentage": 1.8640001},
    "version": 1
  },
  "title": "probe",
  "updated_at": "2026-06-05T07:15:06.201Z"
}`

func writeSidecar(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	transcriptPath := filepath.Join(dir, "abc-123.jsonl")
	if err := os.WriteFile(filepath.Join(dir, "abc-123.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return transcriptPath
}

func TestReadSidecarModelState_LiveShape(t *testing.T) {
	state := readSidecarModelState(writeSidecar(t, liveSidecar), nil)
	if state == nil {
		t.Fatal("expected model state from live-shaped sidecar")
	}
	if state.ModelInfo.ModelID != "auto" {
		t.Errorf("ModelID = %q, want auto", state.ModelInfo.ModelID)
	}
	if state.ModelInfo.ContextWindowTokens != 200000 {
		t.Errorf("ContextWindowTokens = %d, want 200000", state.ModelInfo.ContextWindowTokens)
	}
	if state.ContextUsagePercentage != 1.8640001 {
		t.Errorf("ContextUsagePercentage = %v, want 1.8640001", state.ContextUsagePercentage)
	}
}

// Key order is not guaranteed: rts_model_state may precede the huge
// conversation_metadata blob, and decoys outside session_state must not match.
func TestReadSidecarModelState_KeyOrder(t *testing.T) {
	body := `{
	  "rts_model_state": {"model_info": {"model_id": "decoy-top-level"}},
	  "session_state": {
	    "rts_model_state": {"model_info": {"model_id": "claude-sonnet-4-6", "context_window_tokens": 200000}, "context_usage_percentage": 42.5},
	    "conversation_metadata": {"deep": [1, {"rts_model_state": {"model_info": {"model_id": "decoy-nested"}}}]}
	  }
	}`
	state := readSidecarModelState(writeSidecar(t, body), nil)
	if state == nil {
		t.Fatal("expected model state")
	}
	if state.ModelInfo.ModelID != "claude-sonnet-4-6" {
		t.Errorf("ModelID = %q, want claude-sonnet-4-6 (not a decoy)", state.ModelInfo.ModelID)
	}
}

func TestReadSidecarModelState_Absent(t *testing.T) {
	for name, path := range map[string]string{
		"no sidecar":   filepath.Join(t.TempDir(), "x.jsonl"),
		"non-jsonl":    filepath.Join(t.TempDir(), "history.md"),
		"malformed":    writeSidecar(t, `not json`),
		"no state key": writeSidecar(t, `{"session_state":{"agent_name":"kiro_default"}}`),
		"flat doc":     writeSidecar(t, `{"cwd":"/x"}`),
	} {
		if state := readSidecarModelState(path, nil); state != nil {
			t.Errorf("%s: expected nil, got %+v", name, state)
		}
	}
}

// The walker must skip a conversation_metadata far larger than the decoder
// buffer without decoding it.
func TestReadSidecarModelState_HugeBlobSkipped(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(`{"session_state":{"conversation_metadata":{"turns":[`)
	for i := 0; i < 5000; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(`{"text":"` + strings.Repeat("x", 200) + `"}`)
	}
	sb.WriteString(`]},"rts_model_state":{"model_info":{"model_id":"auto","context_window_tokens":200000},"context_usage_percentage":7.5}}}`)
	state := readSidecarModelState(writeSidecar(t, sb.String()), nil)
	if state == nil || state.ContextUsagePercentage != 7.5 {
		t.Fatalf("expected state with 7.5%% after ~1MB skip, got %+v", state)
	}
}

// The cache must serve an unchanged sidecar from memory (backfill case) and
// invalidate when the file changes (live case: kiro rewrites it every turn).
func TestReadSidecarModelState_Cache(t *testing.T) {
	transcriptPath := writeSidecar(t, liveSidecar)
	sidecar := strings.TrimSuffix(transcriptPath, ".jsonl") + ".json"

	var cache sidecarCache
	first := readSidecarModelState(transcriptPath, &cache)
	if first == nil || cache.state == nil {
		t.Fatal("expected first read to fill the cache")
	}
	if readSidecarModelState(transcriptPath, &cache) != first {
		t.Error("unchanged sidecar: expected the cached pointer back")
	}

	// Change the file (different size guarantees invalidation even when the
	// filesystem's mtime granularity makes back-to-back writes equal-time).
	updated := strings.Replace(liveSidecar, `"model_id": "auto"`, `"model_id": "claude-haiku-4.5"`, 1)
	if err := os.WriteFile(sidecar, []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}
	third := readSidecarModelState(transcriptPath, &cache)
	if third == nil || third.ModelInfo.ModelID != "claude-haiku-4.5" {
		t.Errorf("changed sidecar: got %+v, want re-read with claude-haiku-4.5", third)
	}
}
