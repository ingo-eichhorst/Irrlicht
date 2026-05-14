package sensors

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPTY_capturesRawBytesWithANSI(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pty.raw")
	// CSI 31m = red, CSI 0m = reset. Embed in the file as raw bytes.
	raw := []byte{'h', 'i', 0x1b, '[', '3', '1', 'm', 'X', 0x1b, '[', '0', 'm', '\n'}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s := &PTY{Path: path, PollInterval: 20 * time.Millisecond}
	ch := s.Run(ctx)

	select {
	case sig := <-ch:
		if sig.Sensor != "pty" || sig.Kind != "chunk" {
			t.Errorf("wrong header: %+v", sig)
		}
		var p struct {
			Offset   int64  `json:"offset"`
			Len      int    `json:"len"`
			BytesB64 string `json:"bytes_b64"`
		}
		if err := json.Unmarshal(sig.Payload, &p); err != nil {
			t.Fatal(err)
		}
		got, err := base64.StdEncoding.DecodeString(p.BytesB64)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(raw) {
			t.Errorf("round-trip mismatch:\nwant %v\ngot  %v", raw, got)
		}
		if p.Offset != 0 || p.Len != len(raw) {
			t.Errorf("metadata wrong: offset=%d len=%d", p.Offset, p.Len)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for chunk")
	}
}

func TestPTY_offsetAdvancesAcrossReads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pty.raw")
	if err := os.WriteFile(path, []byte("aaa"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s := &PTY{Path: path, PollInterval: 20 * time.Millisecond}
	ch := s.Run(ctx)

	var (
		seenOffsets []int64
		seenLens    []int
	)
	collect := func(n int) {
		deadline := time.After(time.Second)
		for len(seenOffsets) < n {
			select {
			case sig := <-ch:
				var p struct {
					Offset int64 `json:"offset"`
					Len    int   `json:"len"`
				}
				_ = json.Unmarshal(sig.Payload, &p)
				seenOffsets = append(seenOffsets, p.Offset)
				seenLens = append(seenLens, p.Len)
			case <-deadline:
				t.Fatalf("timed out after %d chunks: %v", len(seenOffsets), seenOffsets)
			}
		}
	}
	collect(1)
	if seenOffsets[0] != 0 || seenLens[0] != 3 {
		t.Errorf("first chunk wrong: offset=%d len=%d", seenOffsets[0], seenLens[0])
	}

	// Append more bytes; next chunk should start at offset 3.
	f, _ := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	f.WriteString("bbbb")
	f.Close()
	collect(2)
	if seenOffsets[1] != 3 || seenLens[1] != 4 {
		t.Errorf("second chunk wrong: offset=%d len=%d", seenOffsets[1], seenLens[1])
	}
}
