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
	fixture := readZstdFixture(t)
	path := filepath.Join(t.TempDir(), "session.v3.jsonl.zstd")
	writeTestFile(t, path, fixture[:len(fixture)-1])

	parser := &dshFixtureParser{}
	tailer := NewTranscriptTailer(path, parser, "dsh")
	tailZstdWithoutError(t, tailer, "torn-frame pass")
	assertZstdTailState(t, tailer, parser, 190, []string{"session"})

	appendTestFile(t, path, fixture[len(fixture)-1:])
	tailZstdWithoutError(t, tailer, "completed-frame pass")
	assertZstdTailState(t, tailer, parser, int64(len(fixture)), []string{"session", "permission/preset", "sandbox/mode", "approval/policy"})
}

func readZstdFixture(t *testing.T) []byte {
	t.Helper()
	fixture, err := os.ReadFile(filepath.Join("testdata", "dsh-session.v3.jsonl.zstd"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return fixture
}

func writeTestFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
}

func appendTestFile(t *testing.T, path string, content []byte) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open fixture for append: %v", err)
	}
	if _, err := f.Write(content); err != nil {
		_ = f.Close()
		t.Fatalf("append fixture: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close fixture: %v", err)
	}
}

func tailZstdWithoutError(t *testing.T, tailer *TranscriptTailer, pass string) {
	t.Helper()
	if _, err := tailer.TailAndProcess(); err != nil {
		t.Fatalf("%s: %v", pass, err)
	}
}

func assertZstdTailState(t *testing.T, tailer *TranscriptTailer, parser *dshFixtureParser, offset int64, types []string) {
	t.Helper()
	if !reflect.DeepEqual(parser.types, types) {
		t.Errorf("parsed types = %v, want %v", parser.types, types)
	}
	if tailer.lastOffset != offset {
		t.Errorf("lastOffset = %d, want %d", tailer.lastOffset, offset)
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
