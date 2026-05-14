package sensors

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

// FS watches a directory tree for create / write / remove / rename
// events. Implementation uses fsnotify (FSEvents on macOS, inotify on
// Linux). Subdirectories created during the recording are added to the
// watcher dynamically.
//
// Scoping is path-based, not process-based: fsnotify gives us file
// events but no causing-PID. The Phase 3 synthesizer correlates these
// signals with proc/transcript timestamps to infer which process did
// the write. That's the right boundary — the kernel APIs that map an
// event to a PID (eBPF, Endpoint Security) are out of scope for v1.
//
// Kind: "create" | "write" | "remove" | "rename" | "chmod".
// Payload: {"path": "<absolute path>"}.
type FS struct {
	// Root is the directory to watch. Subdirectories are added recursively.
	Root string
}

const fsName = "fs"

// Name implements Sensor.
func (f *FS) Name() string { return fsName }

// Run implements Sensor.
func (f *FS) Run(ctx context.Context) <-chan Signal {
	out := make(chan Signal, 32)
	go func() {
		defer close(out)
		w, err := fsnotify.NewWatcher()
		if err != nil {
			return
		}
		defer w.Close()

		// Initial recursive add — walk the tree and watch every directory.
		if err := addRecursively(w, f.Root); err != nil {
			return
		}

		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-w.Events:
				if !ok {
					return
				}
				kind := classifyOp(ev.Op)
				if kind == "" {
					continue
				}
				// Watch any new directories so we keep up with mkdir bursts.
				if kind == "create" {
					if st, err := os.Stat(ev.Name); err == nil && st.IsDir() {
						_ = w.Add(ev.Name)
					}
				}
				payload, _ := MarshalPayload(struct {
					Path string `json:"path"`
				}{Path: ev.Name})
				select {
				case out <- Signal{
					Ts:      time.Now().UTC(),
					Sensor:  fsName,
					Kind:    kind,
					Payload: json.RawMessage(payload),
				}:
				case <-ctx.Done():
					return
				}
			case _, ok := <-w.Errors:
				if !ok {
					return
				}
				// Continue; transient errors shouldn't kill the sensor.
			}
		}
	}()
	return out
}

func addRecursively(w *fsnotify.Watcher, root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrPermission) || errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return w.Add(path)
		}
		return nil
	})
}

func classifyOp(op fsnotify.Op) string {
	switch {
	case op&fsnotify.Create != 0:
		return "create"
	case op&fsnotify.Write != 0:
		return "write"
	case op&fsnotify.Remove != 0:
		return "remove"
	case op&fsnotify.Rename != 0:
		return "rename"
	case op&fsnotify.Chmod != 0:
		return "chmod"
	}
	return ""
}
