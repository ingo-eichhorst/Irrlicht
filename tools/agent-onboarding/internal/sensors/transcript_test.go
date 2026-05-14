package sensors

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTranscript_emitsOneSignalPerLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s := &Transcript{Path: path, PollInterval: 20 * time.Millisecond}
	ch := s.Run(ctx)

	var lines []string
	deadline := time.After(time.Second)
	for len(lines) < 3 {
		select {
		case sig, ok := <-ch:
			if !ok {
				t.Fatalf("channel closed prematurely; saw %v", lines)
			}
			if sig.Sensor != "transcript" || sig.Kind != "line" {
				t.Errorf("unexpected signal: %+v", sig)
			}
			var p struct{ Line string }
			if err := json.Unmarshal(sig.Payload, &p); err != nil {
				t.Fatalf("payload unmarshal: %v", err)
			}
			lines = append(lines, p.Line)
		case <-deadline:
			t.Fatalf("timed out waiting for 3 lines; saw %v", lines)
		}
	}
	if lines[0] != "one" || lines[1] != "two" || lines[2] != "three" {
		t.Errorf("wrong order: %v", lines)
	}
}

func TestTranscript_waitsForFileToAppear(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "later.jsonl")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s := &Transcript{Path: path, PollInterval: 20 * time.Millisecond}
	ch := s.Run(ctx)

	// Sensor should sit and wait — give it 100ms to confirm nothing comes through yet.
	select {
	case sig := <-ch:
		t.Fatalf("got signal before file existed: %+v", sig)
	case <-time.After(100 * time.Millisecond):
	}
	if err := os.WriteFile(path, []byte("late\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case sig := <-ch:
		var p struct{ Line string }
		_ = json.Unmarshal(sig.Payload, &p)
		if p.Line != "late" {
			t.Errorf("got %q", p.Line)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for late line")
	}
}

func TestTranscript_stopsOnCancel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.jsonl")
	if err := os.WriteFile(path, []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Transcript{Path: path, PollInterval: 20 * time.Millisecond}
	ch := s.Run(ctx)
	<-ch // consume the line so the goroutine reaches its ticker wait
	cancel()
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("channel still emitting after cancel")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("channel didn't close within 500ms of cancel")
	}
}
