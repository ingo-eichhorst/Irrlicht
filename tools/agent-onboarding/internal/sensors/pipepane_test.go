package sensors

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPipePane_tailsAppendedLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pipe.log")
	if err := os.WriteFile(path, []byte("first\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s := &PipePane{Path: path, PollInterval: 20 * time.Millisecond}
	ch := s.Run(ctx)

	// First line from initial content.
	select {
	case sig := <-ch:
		var p struct{ Line string }
		_ = json.Unmarshal(sig.Payload, &p)
		if p.Line != "first" || sig.Sensor != "pipepane" || sig.Kind != "line" {
			t.Errorf("first line wrong: %+v / %s", sig, p.Line)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out on first line")
	}

	// Append more — simulating `tmux pipe-pane` writing as the pane scrolls.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("second\nthird\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	var got []string
	deadline := time.After(time.Second)
	for len(got) < 2 {
		select {
		case sig := <-ch:
			var p struct{ Line string }
			_ = json.Unmarshal(sig.Payload, &p)
			got = append(got, p.Line)
		case <-deadline:
			t.Fatalf("timed out, saw only %v", got)
		}
	}
	if got[0] != "second" || got[1] != "third" {
		t.Errorf("wrong order: %v", got)
	}
}
