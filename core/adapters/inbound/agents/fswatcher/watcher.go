// Package fswatcher implements a generic fsnotify-based watcher for agent
// transcript files. It watches a two-level directory tree (root/<project>/<id>.jsonl)
// and emits TranscriptEvents tagged with the adapter name.
package fswatcher

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"irrlicht/core/domain/agent"
)

// Watcher watches a directory tree for .jsonl transcript file events.
// It implements inbound.AgentWatcher.
type Watcher struct {
	root    string        // resolved absolute path to the watched directory
	adapter string        // adapter name set on emitted events
	maxAge  time.Duration // ignore files older than this (0 = no limit)

	subMu sync.Mutex
	subs  []chan agent.Event
}

// New creates a Watcher for the given directory relative to $HOME.
// adapter is the name set on all emitted TranscriptEvents (e.g. "claude-code").
// maxAge controls the maximum file age — transcript files not modified within
// this window are silently ignored. A zero value disables the filter.
func New(relDir, adapter string, maxAge time.Duration) *Watcher {
	home, err := os.UserHomeDir()
	if err != nil {
		return &Watcher{adapter: adapter, maxAge: maxAge}
	}
	return &Watcher{
		root:    filepath.Join(home, relDir),
		adapter: adapter,
		maxAge:  maxAge,
	}
}

// NewWithRoot creates a Watcher targeting a custom absolute root (for testing).
func NewWithRoot(root, adapter string, maxAge time.Duration) *Watcher {
	return &Watcher{root: root, adapter: adapter, maxAge: maxAge}
}

// Root returns the watched directory path.
func (w *Watcher) Root() string { return w.root }

// Adapter returns the adapter name.
func (w *Watcher) Adapter() string { return w.adapter }

// Watch begins watching the directory tree for transcript changes using
// fsnotify (kqueue on macOS). It blocks until ctx is cancelled.
//
// The watcher dynamically adds subdirectories as they appear, so it catches
// new project directories created after Watch starts.
func (w *Watcher) Watch(ctx context.Context) error {
	if w.root == "" {
		<-ctx.Done()
		return ctx.Err()
	}

	// Wait for the root directory to exist.
	if err := w.waitForRoot(ctx); err != nil {
		return err
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer watcher.Close()

	// Add existing project subdirectories.
	if err := w.addExistingDirs(watcher); err != nil {
		return err
	}

	// Also watch the root itself to catch new project directories.
	if err := watcher.Add(w.root); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			w.handleEvent(watcher, ev)
		case _, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			// Transient errors — continue watching.
		}
	}
}

// Subscribe returns a channel that receives transcript events. The channel is
// buffered (capacity 4) so a slow consumer doesn't block the watcher.
func (w *Watcher) Subscribe() <-chan agent.Event {
	ch := make(chan agent.Event, 4)
	w.subMu.Lock()
	w.subs = append(w.subs, ch)
	w.subMu.Unlock()
	return ch
}

// Unsubscribe removes a previously subscribed channel and closes it.
func (w *Watcher) Unsubscribe(ch <-chan agent.Event) {
	w.subMu.Lock()
	defer w.subMu.Unlock()
	for i, s := range w.subs {
		if s == ch {
			w.subs = append(w.subs[:i], w.subs[i+1:]...)
			close(s)
			return
		}
	}
}

// handleEvent processes a single fsnotify event and broadcasts to subscribers.
func (w *Watcher) handleEvent(watcher *fsnotify.Watcher, ev fsnotify.Event) {
	name := ev.Name

	// If a new directory was created under root, start watching it.
	if ev.Op&fsnotify.Create != 0 {
		if info, err := os.Stat(name); err == nil && info.IsDir() {
			// Only watch direct children of the root.
			if filepath.Dir(name) == w.root {
				_ = watcher.Add(name)
			}
			return
		}
	}

	// Only process .jsonl files (transcript files).
	if !strings.HasSuffix(name, ".jsonl") {
		return
	}

	sessionID := extractSessionID(name)
	if sessionID == "" {
		return
	}

	projectDir := filepath.Base(filepath.Dir(name))

	switch {
	case ev.Op&fsnotify.Create != 0:
		size, mtime := fileSizeAndMtime(name)
		if w.maxAge > 0 && !mtime.IsZero() && time.Since(mtime) > w.maxAge {
			return
		}
		w.broadcast(agent.Event{
			Type:           agent.EventNewSession,
			Adapter:        w.adapter,
			SessionID:      sessionID,
			ProjectDir:     projectDir,
			TranscriptPath: name,
			Size:           size,
		})

	case ev.Op&fsnotify.Write != 0:
		size, mtime := fileSizeAndMtime(name)
		if w.maxAge > 0 && !mtime.IsZero() && time.Since(mtime) > w.maxAge {
			return
		}
		w.broadcast(agent.Event{
			Type:           agent.EventActivity,
			Adapter:        w.adapter,
			SessionID:      sessionID,
			ProjectDir:     projectDir,
			TranscriptPath: name,
			Size:           size,
		})

	case ev.Op&(fsnotify.Remove|fsnotify.Rename) != 0:
		w.broadcast(agent.Event{
			Type:           agent.EventRemoved,
			Adapter:        w.adapter,
			SessionID:      sessionID,
			ProjectDir:     projectDir,
			TranscriptPath: name,
			Size:           0,
		})
	}
}

// broadcast sends an event to all subscribers. Non-blocking: drops if consumer
// hasn't drained.
func (w *Watcher) broadcast(ev agent.Event) {
	w.subMu.Lock()
	defer w.subMu.Unlock()
	for _, ch := range w.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// waitForRoot polls until the root directory exists or ctx is cancelled.
// If root already exists, returns immediately.
func (w *Watcher) waitForRoot(ctx context.Context) error {
	if _, err := os.Stat(w.root); err == nil {
		return nil
	}

	// Watch the parent directory for the root dir to be created.
	parent := filepath.Dir(w.root)
	if _, err := os.Stat(parent); err != nil {
		// Even the parent doesn't exist — wait for it by polling
		// the grandparent. Agent CLIs create these on first use.
		grandparent := filepath.Dir(parent)
		return w.waitForDir(ctx, grandparent, w.root)
	}

	return w.waitForDir(ctx, parent, w.root)
}

// waitForDir watches parentDir with fsnotify and returns when targetDir exists.
func (w *Watcher) waitForDir(ctx context.Context, watchDir, targetDir string) error {
	// Ensure the watch directory exists.
	if _, err := os.Stat(watchDir); err != nil {
		// Fall back to blocking — the dir structure doesn't exist at all.
		<-ctx.Done()
		return ctx.Err()
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer watcher.Close()

	if err := watcher.Add(watchDir); err != nil {
		return err
	}

	// Double-check after adding watch (race window).
	if _, err := os.Stat(targetDir); err == nil {
		return nil
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			if ev.Op&fsnotify.Create != 0 {
				if _, err := os.Stat(targetDir); err == nil {
					return nil
				}
			}
		case _, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
		}
	}
}

// addExistingDirs adds fsnotify watches for all existing subdirectories under root.
func (w *Watcher) addExistingDirs(watcher *fsnotify.Watcher) error {
	entries, err := os.ReadDir(w.root)
	if err != nil {
		return nil // root may be empty — not an error
	}
	for _, e := range entries {
		if e.IsDir() {
			_ = watcher.Add(filepath.Join(w.root, e.Name()))
		}
	}
	return nil
}

// --- helpers ----------------------------------------------------------------

// extractSessionID returns the UUID session ID from a .jsonl filename.
// Returns "" if the filename doesn't match the expected pattern.
func extractSessionID(path string) string {
	base := filepath.Base(path)
	if !strings.HasSuffix(base, ".jsonl") {
		return ""
	}
	return strings.TrimSuffix(base, ".jsonl")
}

// fileSize returns the size of a file, or 0 if stat fails.
func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}
