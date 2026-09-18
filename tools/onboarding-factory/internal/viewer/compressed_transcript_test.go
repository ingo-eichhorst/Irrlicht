package viewer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"
)

func writeCompressedTranscript(t *testing.T, dir, lines string) string {
	t.Helper()
	encoder, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer encoder.Close()
	path := filepath.Join(dir, "transcript.jsonl.zstd")
	if err := os.WriteFile(path, encoder.EncodeAll([]byte(lines), nil), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestExtractToolCalls_readsCompressedDSHRecords(t *testing.T) {
	const sessionID = "session-00000000-0000-0000-0000-000000000001"
	path := writeCompressedTranscript(t, t.TempDir(), ""+
		`{"type":"session","id":"`+sessionID+`","createdAt":1000}`+"\n"+
		`{"type":"tool/call","time":2000,"data":{"callId":"call-1","name":"bash","arguments":"{}"}}`+"\n")

	got := extractToolCalls(path)
	if len(got) != 1 {
		t.Fatalf("extractToolCalls() returned %d calls, want 1: %#v", len(got), got)
	}
	if got[0].Name != "bash" || got[0].ID != "call-1" || got[0].SessionID != sessionID || got[0].Ts == "" {
		t.Errorf("tool call = %#v", got[0])
	}
}

func TestNewMetricsEnricher_usesCompressedTranscript(t *testing.T) {
	dir := t.TempDir()
	path := writeCompressedTranscript(t, dir, "{}\n")
	e := newMetricsEnricher(nil, nil, dir)
	if e.transcriptPath != path {
		t.Fatalf("transcriptPath = %q, want %q", e.transcriptPath, path)
	}
}
