// Package gastown implements GT_ROOT detection, resolution, and daemon/state.json
// file-system watching for Gas Town integration.
package gastown

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/fsnotify/fsnotify"

	"irrlicht/core/domain/gastown"
)

// Collector detects Gas Town, resolves GT_ROOT, and watches daemon/state.json.
// It implements inbound.GasTownCollector.
type Collector struct {
	root     string // resolved GT_ROOT path ("" if not detected)
	detected bool

	mu    sync.RWMutex
	state *gastown.DaemonState // latest parsed state

	subMu sync.Mutex
	subs  []chan gastown.DaemonState
}

// New creates a Collector by probing for a Gas Town installation.
// Detection order: GT_ROOT env var → ~/gt (default location).
// A directory is accepted when it contains both daemon/ and rigs.json.
func New() *Collector {
	c := &Collector{}

	root := resolveRoot()
	if root != "" && isGasTownRoot(root) {
		c.root = root
		c.detected = true
		// Best-effort initial read.
		if s, err := readStateFile(stateFilePath(root)); err == nil {
			c.state = s
		}
	}

	return c
}

// Detected returns true if a valid Gas Town installation was found.
func (c *Collector) Detected() bool { return c.detected }

// Root returns the resolved GT_ROOT path, or "" if not detected.
func (c *Collector) Root() string { return c.root }

// DaemonState returns the latest daemon state snapshot, or nil.
func (c *Collector) DaemonState() *gastown.DaemonState {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.state
}

// Watch begins watching daemon/state.json for changes using fsnotify (kqueue
// on macOS). It blocks until ctx is cancelled.
func (c *Collector) Watch(ctx context.Context) error {
	if !c.detected {
		// Nothing to watch — block until context is done.
		<-ctx.Done()
		return ctx.Err()
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer watcher.Close()

	statePath := stateFilePath(c.root)

	// Watch the daemon/ directory so we catch file renames (atomic writes).
	daemonDir := filepath.Dir(statePath)
	if err := watcher.Add(daemonDir); err != nil {
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
			// Only react to writes/creates on state.json itself.
			if filepath.Base(ev.Name) != "state.json" {
				continue
			}
			if ev.Op&(fsnotify.Write|fsnotify.Create) == 0 {
				continue
			}
			c.reload(statePath)
		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			// Log and continue — transient errors are expected.
			_ = err
		}
	}
}

// Subscribe returns a channel that receives a copy of the daemon state on
// every file change. The channel is buffered (1) so a slow consumer doesn't
// block the watcher.
func (c *Collector) Subscribe() <-chan gastown.DaemonState {
	ch := make(chan gastown.DaemonState, 1)
	c.subMu.Lock()
	c.subs = append(c.subs, ch)
	c.subMu.Unlock()
	return ch
}

// Unsubscribe removes a previously subscribed channel and closes it.
func (c *Collector) Unsubscribe(ch <-chan gastown.DaemonState) {
	c.subMu.Lock()
	defer c.subMu.Unlock()
	for i, s := range c.subs {
		if s == ch {
			c.subs = append(c.subs[:i], c.subs[i+1:]...)
			close(s)
			return
		}
	}
}

// reload reads state.json and broadcasts the new state to subscribers.
func (c *Collector) reload(path string) {
	s, err := readStateFile(path)
	if err != nil {
		return // transient — file may be mid-write
	}

	c.mu.Lock()
	c.state = s
	c.mu.Unlock()

	c.subMu.Lock()
	snapshot := *s
	for _, ch := range c.subs {
		// Non-blocking send: drop if consumer hasn't drained.
		select {
		case ch <- snapshot:
		default:
		}
	}
	c.subMu.Unlock()
}

// --- helpers ----------------------------------------------------------------

// resolveRoot returns the GT_ROOT path to probe, preferring the environment
// variable over the default ~/gt location.
func resolveRoot() string {
	if v := os.Getenv("GT_ROOT"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "gt")
}

// isGasTownRoot returns true when dir looks like a valid Gas Town root
// (contains both daemon/ and rigs.json).
func isGasTownRoot(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, "daemon"))
	if err != nil || !info.IsDir() {
		return false
	}
	_, err = os.Stat(filepath.Join(dir, "rigs.json"))
	return err == nil
}

// stateFilePath returns the daemon/state.json path under root.
func stateFilePath(root string) string {
	return filepath.Join(root, "daemon", "state.json")
}

// readStateFile reads and parses daemon/state.json.
func readStateFile(path string) (*gastown.DaemonState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s gastown.DaemonState
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}
