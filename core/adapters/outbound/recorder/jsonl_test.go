package recorder

import (
	"bufio"
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"

	"irrlicht/core/domain/lifecycle"
)

func TestJSONLRecorder_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	rec, err := NewJSONLRecorder(dir)
	if err != nil {
		t.Fatalf("NewJSONLRecorder: %v", err)
	}

	now := time.Now().Truncate(time.Millisecond)
	events := []lifecycle.Event{
		{Seq: 1, Timestamp: now, Kind: lifecycle.KindTranscriptNew, SessionID: "sess-1", Adapter: "claude-code", TranscriptPath: "/tmp/test.jsonl"},
		{Seq: 2, Timestamp: now.Add(time.Second), Kind: lifecycle.KindPIDDiscovered, SessionID: "sess-1", PID: 12345},
		{Seq: 3, Timestamp: now.Add(2 * time.Second), Kind: lifecycle.KindStateTransition, SessionID: "sess-1", PrevState: "ready", NewState: "working", Reason: "transcript activity"},
	}

	for _, ev := range events {
		rec.Record(ev)
	}
	if err := rec.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Read back and verify.
	f, err := os.Open(rec.Path())
	if err != nil {
		t.Fatalf("open recording: %v", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	var got []lifecycle.Event
	for scanner.Scan() {
		var ev lifecycle.Event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		got = append(got, ev)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}

	if len(got) != len(events) {
		t.Fatalf("got %d events, want %d", len(got), len(events))
	}
	for i, ev := range got {
		if ev.Seq != events[i].Seq {
			t.Errorf("event %d: seq=%d, want %d", i, ev.Seq, events[i].Seq)
		}
		if ev.Kind != events[i].Kind {
			t.Errorf("event %d: kind=%s, want %s", i, ev.Kind, events[i].Kind)
		}
		if ev.SessionID != events[i].SessionID {
			t.Errorf("event %d: sessionID=%s, want %s", i, ev.SessionID, events[i].SessionID)
		}
	}
}

func TestJSONLRecorder_ConcurrentWrites(t *testing.T) {
	dir := t.TempDir()
	rec, err := NewJSONLRecorder(dir)
	if err != nil {
		t.Fatalf("NewJSONLRecorder: %v", err)
	}

	const goroutines = 10
	const eventsPerGoroutine = 100

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < eventsPerGoroutine; i++ {
				rec.Record(lifecycle.Event{
					Seq:       int64(gid*eventsPerGoroutine + i),
					Timestamp: time.Now(),
					Kind:      lifecycle.KindTranscriptActivity,
					SessionID: "sess-concurrent",
				})
			}
		}(g)
	}
	wg.Wait()

	if err := rec.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Verify we got all events (no corruption from races).
	f, err := os.Open(rec.Path())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	count := 0
	for scanner.Scan() {
		var ev lifecycle.Event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			t.Fatalf("line %d: unmarshal: %v", count+1, err)
		}
		count++
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}

	want := goroutines * eventsPerGoroutine
	if count != want {
		t.Errorf("got %d events, want %d", count, want)
	}
}

func TestJSONLRecorder_CreatesDir(t *testing.T) {
	dir := t.TempDir()
	nested := dir + "/a/b/c"

	rec, err := NewJSONLRecorder(nested)
	if err != nil {
		t.Fatalf("NewJSONLRecorder: %v", err)
	}
	rec.Record(lifecycle.Event{Seq: 1, Kind: lifecycle.KindTranscriptNew, SessionID: "s"})
	if err := rec.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := os.Stat(rec.Path()); err != nil {
		t.Fatalf("recording file not created: %v", err)
	}
}
