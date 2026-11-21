package workerlist

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"irrlicht/core/domain/orchestrator"
	"irrlicht/core/ports/outbound"
)

// defaultReevaluate is how often the watcher re-reads the file with no change
// event: staleness changes exactly when the file stops changing, so a pool
// whose writer died must still turn stale.
const defaultReevaluate = 30 * time.Second

// logEvent is the event type of every log line this adapter writes.
const logEvent = "orchestrator-workerlist"

// condition is what the last read of the status file found.
type condition int

const (
	condMissing condition = iota // no file: Running=false, the normal "not detected" case
	condOK
	condUnreadable // unreadable, unparsable or invalid: last good snapshot, marked unreadable
)

// Adapter implements inbound.OrchestratorWatcher over one status file.
type Adapter struct {
	path   string
	logger outbound.Logger
	// now and reevaluate are injectable for tests.
	now        func() time.Time
	reevaluate time.Duration

	// Owned by the Watch goroutine (evaluate).
	lastGood *document
	cond     condition

	mu    sync.RWMutex
	state *orchestrator.State

	subMu sync.Mutex
	subs  []chan orchestrator.State
}

// NewAdapter returns a watcher for the status file at path. It reads nothing
// until Watch runs. logger may be nil.
func NewAdapter(path string, logger outbound.Logger) *Adapter {
	return &Adapter{path: path, logger: logger, now: time.Now, reevaluate: defaultReevaluate}
}

// Name returns the orchestrator identifier.
func (a *Adapter) Name() string { return Name }

// Detected reports whether the status file exists (a stat, no read).
func (a *Adapter) Detected() bool { return fileExists(a.path) }

// State returns the latest snapshot, or nil before the first read.
func (a *Adapter) State() *orchestrator.State {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.state == nil {
		return nil
	}
	cp := *a.state
	return &cp
}

// Subscribe returns a channel that receives state snapshots.
func (a *Adapter) Subscribe() <-chan orchestrator.State {
	ch := make(chan orchestrator.State, 1)
	a.subMu.Lock()
	a.subs = append(a.subs, ch)
	a.subMu.Unlock()
	return ch
}

// Unsubscribe removes and closes a subscriber channel.
func (a *Adapter) Unsubscribe(ch <-chan orchestrator.State) {
	a.subMu.Lock()
	defer a.subMu.Unlock()
	for i, s := range a.subs {
		if s == ch {
			a.subs = append(a.subs[:i], a.subs[i+1:]...)
			close(s)
			return
		}
	}
}

// broadcast hands state to every subscriber, replacing a snapshot the
// subscriber has not taken yet: a dropped newest snapshot would leave the
// monitor on an older one until the next change, because evaluate only
// broadcasts on a change.
func (a *Adapter) broadcast(state orchestrator.State) {
	a.subMu.Lock()
	defer a.subMu.Unlock()
	for _, ch := range a.subs {
		select {
		case <-ch:
		default:
		}
		select {
		case ch <- state:
		default:
		}
	}
}

// Watch reads the file, then re-reads it on every change event in its parent
// directory that names it and on every re-evaluation tick, until ctx is
// cancelled. The directory is watched rather than the file for two reasons:
// the file normally does not exist yet when the permission is granted (the
// default path stays empty until a pool writes it), and fsnotify cannot add a
// watch on a missing file (TestAdapter_WatchSeesFileCreatedAfterStart goes
// red when watchDir adds the file); and fsnotify's README warns that a file
// watch is lost when a writer replaces the file atomically. On macOS's kqueue
// backend this fsnotify version re-arms a replaced file by itself, so the
// second reason was not seen to matter here; it is the backends we did not
// run (inotify) it guards. A directory that cannot be watched (it does not
// exist yet) is logged once and retried on each tick; the tick keeps the file
// read meanwhile.
func (a *Adapter) Watch(ctx context.Context) error {
	// A revoke can land before this goroutine first runs; read nothing then.
	if ctx.Err() != nil {
		return ctx.Err()
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		a.logError(fmt.Sprintf("cannot watch %s (re-reading every %v instead): %v", a.path, a.reevaluate, err))
	} else {
		defer w.Close()
	}
	watching := a.watchDir(w, true)
	a.evaluate()

	ticker := time.NewTicker(a.reevaluate)
	defer ticker.Stop()
	events, errs := watcherChans(w)
	base := filepath.Base(a.path)
	for {
		reread, tick := false, false
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-events:
			if !ok {
				events = nil
			}
			reread = ok && filepath.Base(ev.Name) == base
		case _, ok := <-errs:
			// An fsnotify error (e.g. a kqueue overflow) can lose an event;
			// the next tick re-reads the file regardless.
			if !ok {
				errs = nil
			}
		case <-ticker.C:
			reread, tick = true, true
		}
		// select picks at random among ready cases, so after a revoke a
		// pending event or tick could still win once; no read after that.
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if tick && !watching {
			watching = a.watchDir(w, false)
		}
		if reread {
			a.evaluate()
		}
	}
}

// watcherChans returns w's channels, or nil channels (never ready) when w is nil.
func watcherChans(w *fsnotify.Watcher) (<-chan fsnotify.Event, <-chan error) {
	if w == nil {
		return nil, nil
	}
	return w.Events, w.Errors
}

// watchDir adds the status file's parent directory to w, logging a failure
// only on the first attempt so a missing directory is not logged every tick.
func (a *Adapter) watchDir(w *fsnotify.Watcher, first bool) bool {
	if w == nil {
		return false
	}
	dir := filepath.Dir(a.path)
	if err := w.Add(dir); err != nil {
		if first {
			a.logError(fmt.Sprintf("cannot watch %s (re-reading every %v until it can): %v", dir, a.reevaluate, err))
		}
		return false
	}
	return true
}

// evaluate reads the file and stores and broadcasts the resulting snapshot
// when it differs from the last one.
func (a *Adapter) evaluate() {
	next := a.read(a.now())
	a.mu.Lock()
	changed := stateChanged(a.state, next)
	if changed {
		a.state = next
	}
	a.mu.Unlock()
	if changed {
		a.broadcast(*next)
	}
}

// read turns the file's current content into a snapshot as of now:
//   - missing: Running=false, so no group is shown;
//   - unreadable, unparsable or invalid: the last good document, rebuilt as of
//     now with every team marked "unreadable" — or Running=false when there
//     has never been a good one;
//   - valid: the mapped document, which becomes the last good one.
//
// Each change of condition is logged once.
func (a *Adapter) read(now time.Time) *orchestrator.State {
	data, err := os.ReadFile(a.path)
	if errors.Is(err, fs.ErrNotExist) {
		a.lastGood = nil
		a.transition(condMissing, nil)
		return notRunning(now)
	}
	var doc *document
	if err == nil {
		doc, err = decodeDocument(data)
	}
	if err != nil {
		a.transition(condUnreadable, err)
		if a.lastGood == nil {
			return notRunning(now)
		}
		return markUnreadable(buildState(a.lastGood, now))
	}
	a.lastGood = doc
	a.transition(condOK, nil)
	return buildState(doc, now)
}

// transition logs a change of condition once: becoming unreadable as an
// error, any other change as information. Staying in one condition logs
// nothing, and so does the first "missing" — a default path with no file is
// the normal case for a user without a pool.
func (a *Adapter) transition(c condition, err error) {
	if c == a.cond {
		return
	}
	prev := a.cond
	a.cond = c
	switch {
	case c == condUnreadable:
		a.logError(fmt.Sprintf("%s is unreadable, showing the last good snapshot marked unreadable: %v", a.path, err))
	case c == condOK && prev == condUnreadable:
		a.logInfo(fmt.Sprintf("%s is readable again", a.path))
	case c == condOK:
		a.logInfo(fmt.Sprintf("reading worker list %s", a.path))
	case c == condMissing:
		a.logInfo(fmt.Sprintf("%s is gone; the worker pool is hidden", a.path))
	}
}

func notRunning(now time.Time) *orchestrator.State {
	return &orchestrator.State{Adapter: Name, Running: false, UpdatedAt: now}
}

// stateChanged reports whether curr differs from prev in anything but
// UpdatedAt, which moves on every read.
func stateChanged(prev, curr *orchestrator.State) bool {
	if prev == nil {
		return true
	}
	p, c := *prev, *curr
	p.UpdatedAt, c.UpdatedAt = time.Time{}, time.Time{}
	return !reflect.DeepEqual(p, c)
}

func (a *Adapter) logError(msg string) {
	if a.logger != nil {
		a.logger.LogError(logEvent, "", msg)
	}
}

func (a *Adapter) logInfo(msg string) {
	if a.logger != nil {
		a.logger.LogInfo(logEvent, "", msg)
	}
}
