// Package gastown implements GT_ROOT detection, resolution, and daemon/state.json
// + rigs.json file-system watching for Gas Town integration.
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

// Collector detects Gas Town, resolves GT_ROOT, and watches daemon/state.json
// and rigs.json for changes. It implements inbound.GasTownCollector.
type Collector struct {
	root     string // resolved GT_ROOT path ("" if not detected)
	detected bool

	mu    sync.RWMutex
	state *gastown.DaemonState // latest parsed state
	rigs  []gastown.RigState   // latest parsed rigs.json

	subMu sync.Mutex
	subs  []chan gastown.DaemonState

	rigSubMu sync.Mutex
	rigSubs  []chan []gastown.RigState
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
		// Best-effort initial rigs.json read.
		if rigs, err := readRigsFile(rigsFilePath(root)); err == nil {
			c.rigs = rigs
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

// Rigs returns the latest parsed rig definitions from rigs.json.
func (c *Collector) Rigs() []gastown.RigState {
	c.mu.RLock()
	defer c.mu.RUnlock()
	result := make([]gastown.RigState, len(c.rigs))
	copy(result, c.rigs)
	return result
}

// Watch begins watching daemon/state.json and rigs.json for changes using
// fsnotify (kqueue on macOS). It blocks until ctx is cancelled.
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

	// Watch rigs.json (in the GT_ROOT directory).
	if err := watcher.Add(c.root); err != nil {
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
			base := filepath.Base(ev.Name)
			if ev.Op&(fsnotify.Write|fsnotify.Create) == 0 {
				continue
			}
			switch base {
			case "state.json":
				c.reload(statePath)
			case "rigs.json":
				c.reloadRigs(rigsFilePath(c.root))
			}
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

// SubscribeRigs returns a channel that receives updated rig lists when rigs.json changes.
func (c *Collector) SubscribeRigs() <-chan []gastown.RigState {
	ch := make(chan []gastown.RigState, 1)
	c.rigSubMu.Lock()
	c.rigSubs = append(c.rigSubs, ch)
	c.rigSubMu.Unlock()
	return ch
}

// UnsubscribeRigs removes a previously subscribed rigs channel.
func (c *Collector) UnsubscribeRigs(ch <-chan []gastown.RigState) {
	c.rigSubMu.Lock()
	defer c.rigSubMu.Unlock()
	for i, s := range c.rigSubs {
		if s == ch {
			c.rigSubs = append(c.rigSubs[:i], c.rigSubs[i+1:]...)
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

// reloadRigs reads rigs.json and broadcasts the new rig list to subscribers.
func (c *Collector) reloadRigs(path string) {
	rigs, err := readRigsFile(path)
	if err != nil {
		return // transient
	}

	c.mu.Lock()
	c.rigs = rigs
	c.mu.Unlock()

	c.rigSubMu.Lock()
	snapshot := make([]gastown.RigState, len(rigs))
	copy(snapshot, rigs)
	for _, ch := range c.rigSubs {
		select {
		case ch <- snapshot:
		default:
		}
	}
	c.rigSubMu.Unlock()
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

// rigsFilePath returns the rigs.json path under root.
func rigsFilePath(root string) string {
	return filepath.Join(root, "rigs.json")
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

// readRigsFile reads and parses rigs.json.
// rigs.json can be either:
//   - an array of rig objects: [{"name": "irrlicht", ...}, ...]
//   - an object with rig names as keys: {"irrlicht": {...}, ...}
func readRigsFile(path string) ([]gastown.RigState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	// Try array format first.
	var rigs []gastown.RigState
	if err := json.Unmarshal(data, &rigs); err == nil {
		return rigs, nil
	}

	// Try object format: {"rig_name": {fields...}, ...}
	var rigMap map[string]json.RawMessage
	if err := json.Unmarshal(data, &rigMap); err != nil {
		return nil, err
	}

	rigs = make([]gastown.RigState, 0, len(rigMap))
	for name, raw := range rigMap {
		var rig gastown.RigState
		if err := json.Unmarshal(raw, &rig); err != nil {
			continue
		}
		if rig.Name == "" {
			rig.Name = name
		}
		rigs = append(rigs, rig)
	}
	return rigs, nil
}
