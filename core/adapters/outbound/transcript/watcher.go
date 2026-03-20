// Package transcript implements an fsnotify-based watcher for Claude Code
// transcript files under ~/.claude/projects/**.
package transcript

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fsnotify/fsnotify"

	"irrlicht/core/domain/transcript"
)

// projectsDir is the relative path from $HOME to the Claude projects directory.
const projectsDir = ".claude/projects"

// Watcher watches ~/.claude/projects/** for .jsonl transcript file events.
// It implements outbound.TranscriptWatcher.
type Watcher struct {
	root string // resolved absolute path to ~/.claude/projects/

	subMu sync.Mutex
	subs  []chan transcript.TranscriptEvent
}

// New creates a Watcher targeting ~/.claude/projects/.
// The directory does not need to exist yet — Watch will wait for it.
func New() *Watcher {
	home, err := os.UserHomeDir()
	if err != nil {
		return &Watcher{}
	}
	return &Watcher{
		root: filepath.Join(home, projectsDir),
	}
}

// NewWithRoot creates a Watcher targeting a custom root directory (for testing).
func NewWithRoot(root string) *Watcher {
	return &Watcher{root: root}
}

// Root returns the watched projects directory path.
func (w *Watcher) Root() string { return w.root }

// Watch begins watching the projects directory for transcript changes using
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
func (w *Watcher) Subscribe() <-chan transcript.TranscriptEvent {
	ch := make(chan transcript.TranscriptEvent, 4)
	w.subMu.Lock()
	w.subs = append(w.subs, ch)
	w.subMu.Unlock()
	return ch
}

// Unsubscribe removes a previously subscribed channel and closes it.
func (w *Watcher) Unsubscribe(ch <-chan transcript.TranscriptEvent) {
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
			// Only watch direct children of the projects root.
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
		size := fileSize(name)
		w.broadcast(transcript.TranscriptEvent{
			Type:           transcript.EventNewSession,
			SessionID:      sessionID,
			ProjectDir:     projectDir,
			TranscriptPath: name,
			Size:           size,
		})

	case ev.Op&fsnotify.Write != 0:
		size := fileSize(name)
		w.broadcast(transcript.TranscriptEvent{
			Type:           transcript.EventActivity,
			SessionID:      sessionID,
			ProjectDir:     projectDir,
			TranscriptPath: name,
			Size:           size,
		})

	case ev.Op&(fsnotify.Remove|fsnotify.Rename) != 0:
		w.broadcast(transcript.TranscriptEvent{
			Type:           transcript.EventRemoved,
			SessionID:      sessionID,
			ProjectDir:     projectDir,
			TranscriptPath: name,
			Size:           0,
		})
	}
}

// broadcast sends an event to all subscribers. Non-blocking: drops if consumer
// hasn't drained.
func (w *Watcher) broadcast(ev transcript.TranscriptEvent) {
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

	// Watch the parent directory for the projects dir to be created.
	parent := filepath.Dir(w.root)
	if _, err := os.Stat(parent); err != nil {
		// Even the parent doesn't exist — wait for it by polling
		// the grandparent. Claude CLI creates these on first use.
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
