package tailer

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTailAndProcess_LargeToolResultLine exercises the wedge fix from issue
// #270: a JSONL line above bufio.Scanner's old 2 MB cap must not block
// downstream events from being parsed.
func TestTailAndProcess_LargeToolResultLine(t *testing.T) {
	bigContent := strings.Repeat("x", 3*1024*1024) // 3 MB

	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(f)
	must := func(v map[string]interface{}) {
		if err := enc.Encode(v); err != nil {
			t.Fatal(err)
		}
	}
	must(map[string]interface{}{"type": "user", "timestamp": ts(0)})
	// Oversized tool_result, written as a "user" event with a content array
	// (the shape that triggered the live wedge in issue #270).
	must(map[string]interface{}{
		"type": "user", "timestamp": ts(1),
		"message": map[string]interface{}{
			"content": []interface{}{
				map[string]interface{}{
					"type":        "tool_result",
					"tool_use_id": "tu1",
					"content":     bigContent,
				},
			},
		},
	})
	must(map[string]interface{}{
		"type": "assistant", "timestamp": ts(2),
		"message": map[string]interface{}{
			"role": "assistant", "stop_reason": "end_turn",
		},
	})
	must(map[string]interface{}{
		"type": "system", "subtype": "turn_duration", "timestamp": ts(3),
	})
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	tailer := newTestTailer(path)
	m, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatalf("TailAndProcess: %v", err)
	}
	if m.LastEventType != "turn_done" {
		t.Errorf("LastEventType = %q, want %q", m.LastEventType, "turn_done")
	}
}

// TestTailAndProcess_PartialTrailingLine verifies that a transcript ending
// mid-line does not advance the offset past the partial bytes — they must be
// re-read once the writer flushes the rest of the line.
func TestTailAndProcess_PartialTrailingLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	// One complete user line, then partial bytes of a later event with no '\n'.
	if err := json.NewEncoder(f).Encode(map[string]interface{}{
		"type": "user", "timestamp": ts(0),
	}); err != nil {
		t.Fatal(err)
	}
	endOfFirstLine, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		t.Fatal(err)
	}
	partial := []byte(`{"type":"assistant","timestamp":"` + ts(1) + `","message":{"role":"assistant","stop_reason":"end`)
	if _, err := f.Write(partial); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	tailer := newTestTailer(path)
	if _, err := tailer.TailAndProcess(); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if got := tailer.GetLedgerState().LastOffset; got != endOfFirstLine {
		t.Errorf("after partial-line pass: lastOffset = %d, want %d (start of partial bytes)", got, endOfFirstLine)
	}

	// Append the rest of the assistant line plus a turn_duration line.
	f, err = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("_turn\"}}\n")); err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(f).Encode(map[string]interface{}{
		"type": "system", "subtype": "turn_duration", "timestamp": ts(2),
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	m, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if m.LastEventType != "turn_done" {
		t.Errorf("after second pass: LastEventType = %q, want %q", m.LastEventType, "turn_done")
	}
}

// TestReadLineCapped_OversizedLineSkipped exercises the skip path directly
// with a small ceiling so we don't have to round-trip 64 MB through the disk.
func TestReadLineCapped_OversizedLineSkipped(t *testing.T) {
	const max = 1024
	oversized := bytes.Repeat([]byte("a"), max*4) // 4 KB > 1 KB cap
	follow := []byte(`{"type":"user"}`)
	var input bytes.Buffer
	input.Write(oversized)
	input.WriteByte('\n')
	input.Write(follow)
	input.WriteByte('\n')

	r := bufio.NewReaderSize(&input, 256) // small buffer so ReadSlice returns ErrBufferFull
	line, consumed, err := readLineCapped(r, max)
	if !errors.Is(err, errLineTooLong) {
		t.Fatalf("first call: err = %v, want errLineTooLong", err)
	}
	if line != nil {
		t.Errorf("first call: line = %q, want nil", line)
	}
	if want := int64(len(oversized) + 1); consumed != want {
		t.Errorf("first call: consumed = %d, want %d", consumed, want)
	}

	line, consumed, err = readLineCapped(r, max)
	if err != nil {
		t.Fatalf("second call: err = %v, want nil", err)
	}
	if string(line) != string(follow) {
		t.Errorf("second call: line = %q, want %q", line, follow)
	}
	if want := int64(len(follow) + 1); consumed != want {
		t.Errorf("second call: consumed = %d, want %d", consumed, want)
	}

	if _, _, err := readLineCapped(r, max); !errors.Is(err, io.EOF) {
		t.Errorf("third call: err = %v, want io.EOF", err)
	}
}

// TestReadLineCapped_PartialAtEOF asserts that bytes without a trailing '\n'
// are surfaced as errPartialAtEOF with consumed == 0 so the caller does not
// advance past them.
func TestReadLineCapped_PartialAtEOF(t *testing.T) {
	r := bufio.NewReaderSize(bytes.NewReader([]byte(`{"partial":true`)), 256)
	line, consumed, err := readLineCapped(r, 1<<20)
	if !errors.Is(err, errPartialAtEOF) {
		t.Errorf("err = %v, want errPartialAtEOF", err)
	}
	if line != nil {
		t.Errorf("line = %q, want nil", line)
	}
	if consumed != 0 {
		t.Errorf("consumed = %d, want 0", consumed)
	}
}
