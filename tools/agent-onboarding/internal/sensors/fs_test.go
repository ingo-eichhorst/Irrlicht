package sensors

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFS_emitsCreateAndWriteUnderRoot(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s := &FS{Root: root}
	ch := s.Run(ctx)

	// fsnotify needs a beat to initialize before we start writing.
	time.Sleep(100 * time.Millisecond)

	target := filepath.Join(root, "hello.txt")
	if err := os.WriteFile(target, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	saw := map[string]bool{}
	deadline := time.After(2 * time.Second)
	for len(saw) < 1 {
		select {
		case sig, ok := <-ch:
			if !ok {
				t.Fatalf("channel closed, only saw: %v", saw)
			}
			var p struct{ Path string }
			_ = json.Unmarshal(sig.Payload, &p)
			if p.Path == target {
				saw[sig.Kind] = true
			}
		case <-deadline:
			t.Fatalf("timed out; saw: %v", saw)
		}
	}
	// At minimum, expect "create" — Write may or may not arrive depending
	// on the platform's event coalescing, so we don't assert on it.
	if !saw["create"] {
		t.Errorf("expected create event for %s; saw %v", target, saw)
	}
}

func TestFS_picksUpNewSubdir(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s := &FS{Root: root}
	ch := s.Run(ctx)
	time.Sleep(100 * time.Millisecond)

	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	// Drain any events from the mkdir itself.
	time.Sleep(150 * time.Millisecond)
	for {
		select {
		case <-ch:
			continue
		default:
		}
		break
	}

	// Now create a file inside the new subdir. The sensor should have added
	// the subdir to its watcher on the create event above.
	target := filepath.Join(sub, "in-sub.txt")
	if err := os.WriteFile(target, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(2 * time.Second)
	for {
		select {
		case sig := <-ch:
			var p struct{ Path string }
			_ = json.Unmarshal(sig.Payload, &p)
			if p.Path == target && (sig.Kind == "create" || sig.Kind == "write") {
				return
			}
		case <-deadline:
			t.Fatal("did not see event for file in newly-created subdir")
		}
	}
}
