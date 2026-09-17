package tailer

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

type dshFixtureParser struct {
	types []string
}

func TestTranscriptTailer_ZstdTornFrameWaitsAtPriorBoundary(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("testdata", "dsh-session.v3.jsonl.zstd"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	path := filepath.Join(t.TempDir(), "session.v3.jsonl.zstd")
	if err := os.WriteFile(path, fixture[:len(fixture)-1], 0o600); err != nil {
		t.Fatalf("write torn fixture: %v", err)
	}

	parser := &dshFixtureParser{}
	tailer := NewTranscriptTailer(path, parser, "dsh")
	if _, err := tailer.TailAndProcess(); err != nil {
		t.Fatalf("torn-frame pass: %v", err)
	}
	if want := []string{"session"}; !reflect.DeepEqual(parser.types, want) {
		t.Fatalf("torn-frame parsed types = %v, want %v", parser.types, want)
	}
	if tailer.lastOffset != 190 {
		t.Fatalf("torn-frame lastOffset = %d, want first complete frame boundary 190", tailer.lastOffset)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open torn fixture for append: %v", err)
	}
	if _, err := f.Write(fixture[len(fixture)-1:]); err != nil {
		f.Close()
		t.Fatalf("complete final frame: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close completed fixture: %v", err)
	}

	if _, err := tailer.TailAndProcess(); err != nil {
		t.Fatalf("completed-frame pass: %v", err)
	}
	want := []string{"session", "permission/preset", "sandbox/mode", "approval/policy"}
	if !reflect.DeepEqual(parser.types, want) {
		t.Errorf("completed-frame parsed types = %v, want %v", parser.types, want)
	}
	if tailer.lastOffset != int64(len(fixture)) {
		t.Errorf("completed-frame lastOffset = %d, want %d", tailer.lastOffset, len(fixture))
	}
}

func TestTranscriptTailer_ZstdChecksumFailureIsLoud(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("testdata", "dsh-session.v3.jsonl.zstd"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	fixture[len(fixture)-1] ^= 0xff
	path := filepath.Join(t.TempDir(), "session.v3.jsonl.zstd")
	if err := os.WriteFile(path, fixture, 0o600); err != nil {
		t.Fatalf("write corrupt fixture: %v", err)
	}

	parser := &dshFixtureParser{}
	tailer := NewTranscriptTailer(path, parser, "dsh")
	if _, err := tailer.TailAndProcess(); err == nil {
		t.Fatal("TailAndProcess accepted a frame with a corrupt checksum")
	}
	if want := []string{"session"}; !reflect.DeepEqual(parser.types, want) {
		t.Errorf("parsed types = %v, want only the complete frame before the corrupt one: %v", parser.types, want)
	}
	if tailer.lastOffset != 190 {
		t.Errorf("lastOffset = %d, want prior complete frame boundary 190", tailer.lastOffset)
	}
}

func (p *dshFixtureParser) ParseLine(raw map[string]any) *ParsedEvent {
	typeName, _ := raw["type"].(string)
	p.types = append(p.types, typeName)
	return &ParsedEvent{Skip: true}
}

// TestTranscriptTailer_DecodesRealZstdFrames is the red-first proof for
// issue #1980. The fixture is an unmodified session.v3.jsonl.zstd written by
// dsh 0.1.5-rc.2 on 2026-09-17. Before the zstd path existed, the plain-text
// line reader passed compressed bytes to JSON decoding and this test observed
// zero records instead of four.
func TestTranscriptTailer_DecodesRealZstdFrames(t *testing.T) {
	path := filepath.Join("testdata", "dsh-session.v3.jsonl.zstd")
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat fixture: %v", err)
	}

	parser := &dshFixtureParser{}
	tailer := NewTranscriptTailer(path, parser, "dsh")
	if _, err := tailer.TailAndProcess(); err != nil {
		t.Fatalf("TailAndProcess: %v", err)
	}

	wantTypes := []string{"session", "permission/preset", "sandbox/mode", "approval/policy"}
	if len(parser.types) != len(wantTypes) {
		t.Fatalf("parsed types = %v, want %v", parser.types, wantTypes)
	}
	for i := range wantTypes {
		if parser.types[i] != wantTypes[i] {
			t.Errorf("parsed type %d = %q, want %q", i, parser.types[i], wantTypes[i])
		}
	}
	if tailer.lastOffset != stat.Size() {
		t.Errorf("lastOffset = %d, want compressed size %d", tailer.lastOffset, stat.Size())
	}

	if _, err := tailer.TailAndProcess(); err != nil {
		t.Fatalf("second TailAndProcess: %v", err)
	}
	if len(parser.types) != len(wantTypes) {
		t.Errorf("second pass parsed duplicate records: %v", parser.types)
	}
}
