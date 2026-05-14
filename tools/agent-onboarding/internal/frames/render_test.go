package frames

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeRenderer always succeeds and records the calls it received.
type fakeRenderer struct {
	calls int
}

func (r *fakeRenderer) Capture(ctx context.Context, outDir string, ts time.Time) (FrameIndex, error) {
	r.calls++
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return FrameIndex{}, err
	}
	name := ts.Format("20060102T150405.000000000Z") + ".txt"
	full := filepath.Join(outDir, name)
	if err := os.WriteFile(full, []byte("frame content\n"), 0o644); err != nil {
		return FrameIndex{}, err
	}
	return FrameIndex{Ts: ts.UTC(), Path: name, Format: "text"}, nil
}

func TestRun_writesFramesAndIndex(t *testing.T) {
	outDir := t.TempDir()
	r := &fakeRenderer{}
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error)
	go func() { done <- Run(ctx, outDir, r, 50*time.Millisecond) }()

	// Give the loop enough wall time to produce 3 frames.
	time.Sleep(200 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	if r.calls < 2 {
		t.Errorf("expected at least 2 captures, got %d", r.calls)
	}

	// Verify the index.
	idx, err := os.Open(filepath.Join(outDir, "frames.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	scanner := bufio.NewScanner(idx)
	count := 0
	for scanner.Scan() {
		var e FrameIndex
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			t.Fatalf("bad index line %q: %v", scanner.Text(), err)
		}
		if !strings.HasPrefix(e.Path, "frames/") {
			t.Errorf("path not relative-to-outDir: %s", e.Path)
		}
		if e.Format != "text" {
			t.Errorf("wrong format: %s", e.Format)
		}
		if e.Ts.IsZero() {
			t.Error("ts not set")
		}
		count++
	}
	if count != r.calls {
		t.Errorf("index has %d rows but captures saw %d", count, r.calls)
	}
}

// TestTextRenderer_invalidTargetErrors confirms that when tmux can't reach
// the target (or isn't installed), the renderer returns an error rather
// than crashing — which the recorder's loop swallows.
func TestTextRenderer_invalidTargetErrors(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r := &TextRenderer{Target: "definitely-not-a-real-tmux-session-12345:0.0"}
	_, err := r.Capture(ctx, t.TempDir(), time.Now())
	if err == nil {
		t.Fatal("expected error from invalid tmux target")
	}
}
