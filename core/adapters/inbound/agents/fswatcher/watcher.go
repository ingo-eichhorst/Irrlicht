// Package fswatcher implements a generic fsnotify-based watcher for agent
// transcript files. It watches a directory tree recursively, to unbounded depth
// (root/<project>/<id>.jsonl is the common shape, but nested layouts — Claude
// Code's subagents/, gemini's chats/<uuid>/, vibe's agents/, antigravity's
// 3-deep tree — are covered by the same recursion), and emits TranscriptEvents
// tagged with the adapter name.
package fswatcher

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"irrlicht/core/domain/agent"
	"irrlicht/core/ports/outbound"
)

// transcriptExt is the file extension for agent transcript files.
const transcriptExt = ".jsonl"

// defaultReconcileInterval is how often Watch re-walks the tree to catch
// anything fsnotify never reported. It bounds the blind spot left by a
// dropped kernel notification (issue #1248) without turning the watcher into
// a poller: notifications remain the fast path and this is the safety net.
//
// The cost of one sweep is a ReadDir per directory plus a stat per transcript
// file — every transcript ever written, not just the recent ones, because
// maxAge is applied after the stat rather than pruning the walk. On a
// heavily-used machine three of the eight roots alone hold ~794 directories
// and ~1541 transcripts, of which only ~101 are inside the 5-day window; treat
// that as a lower bound on the per-sweep cost, not a total. It is cheap enough
// at this interval, but it scales with total history rather than with active
// sessions, so pruning the descent (skipping directories whose mtime hasn't
// moved, and stat-ing only known paths for growth) is the prerequisite for
// shortening this.
const defaultReconcileInterval = 15 * time.Second

// Watcher watches a directory tree for .jsonl transcript file events.
// It implements inbound.Watcher.
type Watcher struct {
	root     string          // resolved absolute path to the watched directory
	adapter  string          // adapter name set on emitted events
	identity agent.Identity  // populated via WithIdentity
	maxAge   time.Duration   // ignore files older than this (0 = no limit)
	logger   outbound.Logger // reports failed watch registrations (may be nil)

	// sessionID, when non-nil, overrides how a transcript file's session ID is
	// derived from its path (set via WithSessionID). The default uses the
	// filename stem. Returning "" skips the file — adapters whose transcript
	// filename is constant use this both to source the ID from a path component
	// and to ignore sibling files (e.g. Antigravity's transcript_full.jsonl).
	sessionID func(path string) string
	// parentSessionID, when non-nil, derives a child transcript's parent from
	// its contents before the event is emitted. The callback is only enabled
	// for adapters that persist an authoritative header relationship.
	parentSessionID func(path string) string
	// pendingNew holds zero-byte Create events for header-linked adapters.
	// Deferring them until the first Write lets the adapter read the metadata
	// header and prevents a child from briefly appearing as a top-level row.
	pendingNew map[string]struct{}

	// reconcileInterval overrides how often the reconcile sweep runs (see
	// WithReconcileInterval). Zero means defaultReconcileInterval; negative
	// disables the sweep.
	reconcileInterval time.Duration
	// emitted records the size last broadcast for each transcript path, so the
	// reconcile sweep can distinguish state fsnotify already reported from
	// state it missed. Entries are removed when a file is deleted, so a
	// recreated path is reported as a new session again. It is otherwise
	// bounded by the number of transcript files this run has reported on —
	// the same order as the tree the watcher already walks — so it is not
	// pruned on any other schedule.
	emitted map[string]int64
	// watched records directories already registered with fsnotify, so the
	// reconcile sweep re-arms only the ones that are genuinely new — and
	// retries any whose earlier Add failed.
	watched map[string]struct{}
	// watchFailed records directories whose rejected registration has already
	// been reported, so the sweep's retry doesn't re-log the same blind spot
	// every interval.
	//
	// pendingNew, emitted, watched and watchFailed are all confined to Watch's
	// goroutine (handleEvent, the backlog scan and reconcile all run on it), so
	// none of them take a lock.
	watchFailed map[string]struct{}

	subMu sync.Mutex
	subs  []chan agent.Event

	readyMu   sync.Mutex
	readyOnce sync.Once
	ready     chan struct{} // closed once the root watch is attached
}

// Ready returns a channel that is closed once Watch has attached the
// underlying fsnotify watch to the root (and every pre-existing subdir, with
// every pre-existing transcript file already emitted). After it is readable,
// file mutations under the tree are guaranteed to be observed — tests and
// wiring can wait on it instead of sleeping a guessed interval to dodge the
// attach race. The channel never sends; it is only closed. Safe to call
// before, during, or after Watch.
//
// This is unchanged from before issue #998's fix: the root directory itself
// is actually watched earlier still — see Watch — specifically so a
// brand-new top-level directory created while the (potentially slow, large)
// historical backlog scan is still running is never silently missed,
// independent of how big that backlog is. Ready() deliberately continues to
// wait for the full scan too (rather than firing at that earlier point), so
// every existing caller's guarantee — including writing into a pre-existing
// subdirectory right after Ready() fires — is preserved exactly as before.
//
// The guarantee is bounded by what could actually be armed. Arming the root
// is a hard precondition — Watch returns an error if it fails — but an
// individual pre-existing subdir can still be rejected by fsnotify (FD
// exhaustion, a permissions change, Linux's inotify watch limit). The scan
// then continues and Ready() still fires: one unreadable directory must not
// strand the whole watcher. What that costs is a blind spot under that one
// subtree, so addWatch reports every rejection through the Logger set by
// WithLogger rather than discarding it (#1255), and the reconcile sweep
// retries the registration so the blind spot closes on its own if whatever
// rejected it clears (#1248).
func (w *Watcher) Ready() <-chan struct{} { return w.readyChan() }

// readyChan lazily creates the ready channel so the literal constructors
// (New, NewWithRoot) don't each have to initialize it.
func (w *Watcher) readyChan() chan struct{} {
	w.readyMu.Lock()
	defer w.readyMu.Unlock()
	if w.ready == nil {
		w.ready = make(chan struct{})
	}
	return w.ready
}

// signalReady closes the ready channel exactly once.
func (w *Watcher) signalReady() {
	w.readyOnce.Do(func() { close(w.readyChan()) })
}

// WithIdentity sets the full agent.Identity for this watcher so it
// satisfies inbound.Watcher. Returns the watcher for chaining. Callers
// in cmd/irrlichd/wiring.go invoke this immediately after New(); test
// callers (e2e) may omit it because they don't consume the new port.
func (w *Watcher) WithIdentity(id agent.Identity) *Watcher {
	w.identity = id
	return w
}

// Identity returns the agent.Identity supplied via WithIdentity, or the
// zero value if WithIdentity was never called.
func (w *Watcher) Identity() agent.Identity {
	return w.identity
}

// WithLogger sets the logger used to report watch registrations that fail
// during the tree scan. Returns the watcher for chaining. cmd/irrlichd/
// wiring.go supplies the daemon's logger; test and e2e callers may leave it
// nil, in which case a failure stays non-fatal but goes unreported.
func (w *Watcher) WithLogger(l outbound.Logger) *Watcher {
	w.logger = l
	return w
}

// WithSessionID sets a custom session-ID extractor (see the sessionID field).
// Returns the watcher for chaining. Callers in cmd/irrlichd/wiring.go invoke
// this for FilesUnderRoot adapters that declare SessionIDFromPath.
func (w *Watcher) WithSessionID(fn func(path string) string) *Watcher {
	w.sessionID = fn
	return w
}

// WithParentSessionID sets a transcript-header parent-ID extractor. A
// zero-byte Create is deferred until the first Write so the callback can read
// the header before the new-session event reaches the session detector.
func (w *Watcher) WithParentSessionID(fn func(path string) string) *Watcher {
	w.parentSessionID = fn
	return w
}

// WithReconcileInterval overrides how often Watch runs the reconcile sweep
// that catches transcript files fsnotify never reported. Zero (the default)
// uses defaultReconcileInterval; a negative value disables the sweep entirely,
// leaving fsnotify as the only source of events. Returns the watcher for
// chaining. Tests use it to pull the safety net inside their own deadline;
// production wiring leaves it at the default.
func (w *Watcher) WithReconcileInterval(d time.Duration) *Watcher {
	w.reconcileInterval = d
	return w
}

// idFor derives a transcript file's session ID using the custom extractor when
// one is set, otherwise the default filename-stem rule. Returning "" means the
// file should be skipped.
func (w *Watcher) idFor(path string) string {
	if w.sessionID != nil {
		return w.sessionID(path)
	}
	return extractSessionID(path)
}

func (w *Watcher) parentIDFor(path string) string {
	if w.parentSessionID == nil {
		return ""
	}
	return w.parentSessionID(path)
}

func (w *Watcher) eventFor(typ agent.EventType, sessionID, projectDir, path string, size int64) agent.Event {
	return agent.Event{
		Type:            typ,
		SessionID:       sessionID,
		ProjectDir:      projectDir,
		TranscriptPath:  path,
		Size:            size,
		ParentSessionID: w.parentIDFor(path),
	}
}

// New creates a Watcher for the given directory. If dir is absolute, it is
// used as-is; otherwise it is resolved relative to $HOME. Absolute paths let
// adapters honor upstream env-var overrides (e.g. PI_CODING_AGENT_SESSION_DIR,
// CLAUDE_CONFIG_DIR, CODEX_HOME) without coupling fswatcher to any specific
// agent's environment conventions.
//
// adapter is the name set on all emitted TranscriptEvents (e.g. "claude-code").
// maxAge controls the maximum file age — transcript files not modified within
// this window are silently ignored. A zero value disables the filter.
func New(dir, adapter string, maxAge time.Duration) *Watcher {
	if filepath.IsAbs(dir) {
		return &Watcher{root: filepath.Clean(dir), adapter: adapter, maxAge: maxAge}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return &Watcher{adapter: adapter, maxAge: maxAge}
	}
	return &Watcher{
		root:    filepath.Join(home, dir),
		adapter: adapter,
		maxAge:  maxAge,
	}
}

// NewWithRoot creates a Watcher targeting a custom absolute root, bypassing
// the $HOME-relative resolution New() applies. Intended for tests (including
// cross-package e2e tests) that need to drive the watcher against a temp dir.
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

	w.resetRunState()

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer watcher.Close()

	// Watch the root itself first — before scanning any pre-existing
	// subdirectories below — so the kernel starts queuing Create events for
	// brand-new top-level directories immediately, rather than only once the
	// historical scan finishes. Arming the root is O(1), unlike the scan
	// below, so this alone bounds how long a brand-new top-level directory
	// can go unnoticed independent of backlog size (issue #998). This does
	// NOT move signalReady() earlier — see Ready()'s doc comment for why
	// that guarantee is deliberately left where it was.
	if err := watcher.Add(w.root); err != nil {
		return err
	}

	// Recursively add watches for pre-existing subdirectories and emit
	// EventNewSession for transcript files that were already on disk.
	// Newest-first (see addExistingDirs) so a directory that already existed
	// when this scan started, but sorts last lexically (e.g. a
	// timestamp-prefixed session dir), is still processed near the front
	// rather than dead last. Any Create event queued by the kernel for a
	// directory made after the scan started — caught by the root watch
	// armed above — is drained between each directory instead of waiting
	// for the whole backlog to finish (see drainPendingEvents), so a
	// brand-new top-level directory's discovery latency doesn't scale with
	// backlog size (issue #998).
	if err := w.addExistingDirs(ctx, watcher); err != nil {
		return err
	}

	// The watch is now live for the root and every pre-existing subdir, and
	// every pre-existing transcript file has been emitted; unblock anyone
	// waiting on Ready() before mutating files.
	w.signalReady()

	reconcileC, stopReconcile := w.reconcileTicker()
	defer stopReconcile()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-reconcileC:
			w.reconcile(ctx, watcher)
		case ev, ok := <-watcher.Events:
			if !w.dispatchEvent(watcher, ev, ok) {
				return nil
			}
		case _, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			// Transient errors — continue watching.
		}
	}
}

// resetRunState starts a Watch run's bookkeeping from empty, so a Watcher
// driven twice doesn't inherit the previous run's view of what was already
// emitted or already armed. pendingNew is left nil because its write sites
// allocate it lazily.
func (w *Watcher) resetRunState() {
	w.emitted = make(map[string]int64)
	w.watched = make(map[string]struct{})
	w.watchFailed = make(map[string]struct{})
	w.pendingNew = nil
}

// reconcileTicker returns the channel Watch selects on to run the sweep that
// periodically re-walks the tree, plus the func to stop it.
//
// The sweep exists because fsnotify's macOS kqueue backend discovers new files
// by diffing a directory listing rather than receiving a per-file
// notification, so a creation can be missed outright — the failure mode behind
// issue #1248, where the symptom is an event that never arrives rather than
// one that arrives late. A dropped notification would otherwise leave that
// session invisible for the daemon's whole lifetime; this bounds it to one
// interval.
//
// When the sweep is disabled the channel is nil, which parks Watch's reconcile
// select case forever, and the stop func is a no-op — so the caller needs no
// conditional of its own.
func (w *Watcher) reconcileTicker() (<-chan time.Time, func()) {
	interval := w.effectiveReconcileInterval()
	if interval <= 0 {
		return nil, func() {}
	}
	ticker := time.NewTicker(interval)
	return ticker.C, ticker.Stop
}

// effectiveReconcileInterval resolves the configured sweep interval: zero
// means the default, negative means disabled (reported as zero).
func (w *Watcher) effectiveReconcileInterval() time.Duration {
	switch {
	case w.reconcileInterval == 0:
		return defaultReconcileInterval
	case w.reconcileInterval < 0:
		return 0
	default:
		return w.reconcileInterval
	}
}

// dispatchEvent handles a single value received from watcher.Events: ok
// false means the channel is closed (report that to the caller so it can
// stop), otherwise the event is passed to handleEvent. Shared by Watch's
// main loop and drainPendingEvents so the two dispatch paths can't drift
// apart.
func (w *Watcher) dispatchEvent(watcher *fsnotify.Watcher, ev fsnotify.Event, ok bool) bool {
	if !ok {
		return false
	}
	w.handleEvent(watcher, ev)
	return true
}

// Subscribe returns a channel that receives transcript events. The channel is
// buffered so a slow consumer doesn't block the watcher. The capacity must be
// large enough to absorb bursts from concurrent sessions and subagent
// transcripts — all files in the watched tree share this single channel, and
// broadcast silently drops events when the channel is full.
func (w *Watcher) Subscribe() <-chan agent.Event {
	ch := make(chan agent.Event, 64)
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

	if ev.Op&fsnotify.Create != 0 && w.handleDirCreate(watcher, name) {
		return
	}

	if ev.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
		// A path that goes away takes any fsnotify watch on it with it, so
		// forget it here: otherwise a directory recreated at the same path is
		// short-circuited by addWatch as already-watched and never re-armed,
		// leaving that subtree reachable only by the reconcile sweep. A
		// no-op for transcript files, which are never in these maps.
		delete(w.watched, name)
		delete(w.watchFailed, name)
	}

	// Only process .jsonl files (transcript files).
	if !strings.HasSuffix(name, transcriptExt) {
		return
	}

	sessionID := w.idFor(name)
	if sessionID == "" {
		return
	}

	projectDir := filepath.Base(filepath.Dir(name))

	switch {
	case ev.Op&fsnotify.Create != 0:
		w.handleTranscriptCreate(name, sessionID, projectDir)

	case ev.Op&fsnotify.Write != 0:
		w.handleTranscriptWrite(name, sessionID, projectDir)

	case ev.Op&(fsnotify.Remove|fsnotify.Rename) != 0:
		delete(w.pendingNew, name)
		// Forget the emitted size too, so a path that is recreated later is
		// reported as a new session rather than as activity.
		delete(w.emitted, name)
		w.broadcast(w.eventFor(agent.EventRemoved, sessionID, projectDir, name, 0))
	}
}

// handleTranscriptCreate emits EventNewSession for a newly created transcript,
// unless it is stale or is the zero-byte create of a header-linked adapter — the
// latter is parked in pendingNew so the first Write can emit it once the
// metadata header is readable (see the pendingNew field comment).
func (w *Watcher) handleTranscriptCreate(name, sessionID, projectDir string) {
	size, mtime := fileSizeAndMtime(name)
	if w.isStale(mtime) {
		return
	}
	if size == 0 && w.parentSessionID != nil {
		if w.pendingNew == nil {
			w.pendingNew = make(map[string]struct{})
		}
		w.pendingNew[name] = struct{}{}
		return
	}
	w.emit(agent.EventNewSession, sessionID, projectDir, name, size)
}

// handleTranscriptWrite emits EventActivity for a write to a known transcript,
// or the EventNewSession deferred by handleTranscriptCreate when this is the
// first write to a parked zero-byte file.
func (w *Watcher) handleTranscriptWrite(name, sessionID, projectDir string) {
	size, mtime := fileSizeAndMtime(name)
	if w.isStale(mtime) {
		return
	}
	if _, pending := w.pendingNew[name]; pending {
		delete(w.pendingNew, name)
		w.emit(agent.EventNewSession, sessionID, projectDir, name, size)
		return
	}
	w.emit(agent.EventActivity, sessionID, projectDir, name, size)
}

// handleDirCreate handles a Create event that names a directory: starts
// watching it and any subdirectories it already contains, and reports
// whether the event was for a directory (in which case handleEvent should
// stop — directories are never transcript files). A recursive walk is
// required because the directory may already contain nested subdirectories
// by the time we process the event — e.g. Claude Code creates <session>/,
// <session>/subagents/, and the subagent files inside it in rapid
// succession, and our handler runs late enough that only <session>/ appears
// in the fsnotify event stream. Without the recursive walk, the nested
// subagents/ dir never gets a watch and every subagent transcript is
// silently missed. Split out of handleEvent (go:S3776).
func (w *Watcher) handleDirCreate(watcher *fsnotify.Watcher, name string) bool {
	info, err := os.Stat(name)
	if err != nil || !info.IsDir() {
		return false
	}
	if strings.HasPrefix(name, w.root) {
		w.addSubtree(watcher, name)
	}
	return true
}

// isStale reports whether mtime is older than the watcher's maxAge cutoff.
// A non-positive maxAge disables the cutoff.
func (w *Watcher) isStale(mtime time.Time) bool {
	return w.maxAge > 0 && !mtime.IsZero() && time.Since(mtime) > w.maxAge
}

// emit records the size being reported for path and broadcasts the event.
// Every new-session and activity emission goes through here so the reconcile
// sweep can tell the state fsnotify already reported from the state it missed,
// and therefore never re-reports a file the normal event path handled.
// EventRemoved deliberately does not: it deletes the entry instead.
func (w *Watcher) emit(typ agent.EventType, sessionID, projectDir, path string, size int64) {
	// The guard is symmetric: the sweep skips what fsnotify already reported,
	// and this skips what the sweep already reported. Both directions matter,
	// because fsnotify's event channel is unbuffered — a Create can sit blocked
	// in the producer while a sweep runs, walks the same file and reports it,
	// and only then get delivered to the ordinary create path. Restricted to
	// new-session events: a repeated activity event is absorbed downstream by
	// the detector's debounce, and suppressing one could hide a real write.
	if prev, ok := w.emitted[path]; ok && prev == size && typ == agent.EventNewSession {
		return
	}
	if w.emitted == nil {
		w.emitted = make(map[string]int64)
	}
	w.emitted[path] = size
	w.broadcast(w.eventFor(typ, sessionID, projectDir, path, size))
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

// addExistingDirs adds fsnotify watches for all subdirectories under root and
// emits EventNewSession for any transcript files that already exist. Without
// the emit step, transcript files that were written before the daemon
// started and receive no further writes would stay invisible until the next
// write event — e.g. an idle Codex session that the user hasn't typed into
// since before a daemon restart.
//
// Root's direct children are visited newest-mtime-first rather than in
// filepath.WalkDir's lexical order: several adapters (e.g. mistral-vibe) name
// session directories with a sortable timestamp prefix, so a brand-new
// directory always sorts last lexically — on a machine with a large backlog
// of old sessions that pushed discovery of a directory that already existed
// when this scan began out by however long the rest of the backlog took to
// scan (issue #998). Newest-first bounds that cost independent of backlog
// size.
//
// Between each directory, drainPendingEvents processes any fsnotify events
// already queued for the live root watch Watch armed before calling this
// method. kqueue's Events channel is unbuffered (fsnotify's default buffer
// size on this platform is 0), so without this step a directory created
// elsewhere while this scan is still running would simply block undelivered
// until the scan's for-loop below finishes and Watch's own select loop
// regains control — i.e. still bounded by total backlog size, the exact
// defect this method exists to fix. Draining between directories instead
// bounds that wait to roughly one directory's worth of scan work.
//
// ctx is checked between directories (not mid-directory) so a cancelled
// Watch exits promptly without waiting out a large remaining backlog, while
// keeping the loop body simple.
func (w *Watcher) addExistingDirs(ctx context.Context, watcher *fsnotify.Watcher) error {
	entries, err := os.ReadDir(w.root)
	if err != nil {
		return nil // root existence already confirmed by waitForRoot; treat as empty
	}
	// Root's own transcript files, before descending. The root is watched but
	// is never itself a WalkDir start point below, so without this a flat
	// layout — kiro-cli keeps <uuid>.jsonl directly under sessions/cli with no
	// project directory — has its entire pre-existing backlog skipped, and
	// Ready()'s "every pre-existing transcript file has already been emitted"
	// is false for that adapter. The reconcile sweep applies the same rule on
	// a timer; this is what makes that a schedule rather than a special case.
	w.emitExistingFiles(w.root)
	for _, dir := range newestFirst(w.root, entries) {
		if ctx.Err() != nil {
			return nil // Watch's own select loop will observe ctx.Done() and return
		}
		_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil // skip unreadable dirs
			}
			if d.IsDir() {
				w.addWatch(watcher, path)
				w.emitExistingFiles(path)
			}
			return nil
		})
		w.drainPendingEvents(watcher)
	}
	return nil
}

// addWatch registers dir with the fsnotify watcher. The single place either
// tree scan (addExistingDirs at startup, addSubtree at runtime) arms a
// directory, so the two can't drift apart in how they treat a rejected
// registration.
//
// A failure — FD exhaustion (EMFILE), a permissions change, Linux's inotify
// watch limit — leaves that directory unwatched while the rest of the tree
// carries on, and every transcript written under it is then simply never
// observed. It deliberately does not abort the scan (one unreadable
// directory must not strand the whole watcher, which is why the error was
// originally discarded), but it is reported through the Logger per Key
// Conventions' logged-not-propagated rule, so the blind spot leaves a trace
// instead of none at all (#1255).
// A rejected directory is retried by the next reconcile sweep, because only a
// successful registration is recorded in watched — so a watch lost to a
// transient condition recovers on its own instead of staying blind for the
// daemon's lifetime. The rejection is reported once per directory rather than
// once per retry (watchFailed), so a genuinely unreadable directory leaves a
// trace without repeating it every sweep; clearing the entry on success means
// a later failure is reported again.
func (w *Watcher) addWatch(watcher *fsnotify.Watcher, dir string) {
	if _, ok := w.watched[dir]; ok {
		return // already armed — re-registering every sweep would be churn
	}
	err := watcher.Add(dir)
	if err == nil {
		if w.watched == nil {
			w.watched = make(map[string]struct{})
		}
		w.watched[dir] = struct{}{}
		delete(w.watchFailed, dir)
		return
	}
	if _, reported := w.watchFailed[dir]; reported {
		return
	}
	if w.watchFailed == nil {
		w.watchFailed = make(map[string]struct{})
	}
	w.watchFailed[dir] = struct{}{}
	if w.logger == nil {
		return
	}
	w.logger.LogError("fswatcher", "", fmt.Sprintf(
		"%s: failed to watch %s — transcript changes under it will not be observed: %v",
		w.adapter, dir, err))
}

// drainPendingEvents processes any fsnotify events already queued on
// watcher's Events/Errors channels, without blocking if neither has one
// ready. Called between each directory of the historical backlog scan in
// addExistingDirs so a directory created elsewhere while that scan is still
// running — most importantly a brand-new top-level directory, since the
// root watch is armed before the scan starts — is handled promptly instead
// of waiting for the entire remaining backlog to finish (issue #998).
func (w *Watcher) drainPendingEvents(watcher *fsnotify.Watcher) {
	for {
		select {
		case ev, ok := <-watcher.Events:
			if !w.dispatchEvent(watcher, ev, ok) {
				return
			}
		case _, ok := <-watcher.Errors:
			if !ok {
				return
			}
			// Transient errors — the main loop handles them the same way
			// once this scan finishes; nothing more to do here.
		default:
			return
		}
	}
}

// newestFirst returns the absolute paths of root's directory entries, sorted
// by modification time descending (newest first). Non-directory entries are
// skipped — a flat layout's transcripts (kiro-cli) are handled by the caller
// before it descends, not here; an entry whose mtime can't be read sorts as if
// it were the oldest rather than aborting the scan.
//
// Only the startup scan uses this. The ordering exists to bound *first*
// discovery latency over a one-shot backlog (#998); the reconcile sweep visits
// every directory unconditionally and repeats, so ordering changes nothing
// observable there and it walks entries directly instead.
func newestFirst(root string, entries []os.DirEntry) []string {
	type dirMtime struct {
		path  string
		mtime time.Time
	}
	dirs := make([]dirMtime, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		var mtime time.Time
		if info, err := e.Info(); err == nil {
			mtime = info.ModTime()
		}
		dirs = append(dirs, dirMtime{path: filepath.Join(root, e.Name()), mtime: mtime})
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].mtime.After(dirs[j].mtime) })
	paths := make([]string, len(dirs))
	for i, d := range dirs {
		paths[i] = d.path
	}
	return paths
}

// addSubtree recursively adds fsnotify watches for dir and every
// subdirectory already beneath it, and emits EventNewSession for every
// existing .jsonl file it finds. Used from handleEvent when a new
// directory appears at runtime; covers the case where the new dir was
// created together with nested subdirs and files that already exist by
// the time our handler processes the fsnotify Create event.
func (w *Watcher) addSubtree(watcher *fsnotify.Watcher, dir string) {
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			w.addWatch(watcher, path)
			w.emitExistingFiles(path)
		}
		return nil
	})
}

// emitExistingFiles scans a newly-watched directory for .jsonl files that were
// created before the watch was added and emits EventNewSession for each.
func (w *Watcher) emitExistingFiles(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	projectDir := filepath.Base(dir)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), transcriptExt) {
			continue
		}
		fullPath := filepath.Join(dir, e.Name())
		sessionID := w.idFor(fullPath)
		if sessionID == "" {
			continue
		}
		size, mtime := fileSizeAndMtime(fullPath)
		if w.isStale(mtime) {
			continue
		}
		w.emit(agent.EventNewSession, sessionID, projectDir, fullPath, size)
	}
}

// reconcile re-walks the tree and reports anything fsnotify never did: a
// transcript file with no emission recorded for it becomes an EventNewSession,
// and one whose size has moved since the last emission becomes an
// EventActivity. In the healthy case every file's recorded size already
// matches what is on disk and the sweep emits nothing at all, so this is
// invisible unless a notification was genuinely lost (issue #1248).
//
// Deletions are deliberately not reconciled. Inferring EventRemoved from a
// file's absence would turn any transient ReadDir failure into a spurious
// teardown of every session under that directory, and a lost Remove is far
// less costly than a lost creation — the session is stale rather than
// invisible.
//
// Like the backlog scan in addExistingDirs, this drains fsnotify's (unbuffered
// on kqueue) event channel between top-level directories so a live event isn't
// left blocked behind a long walk, and checks ctx there so cancellation isn't
// delayed by the rest of the sweep. It runs on Watch's goroutine, which is what
// makes its use of pendingNew/emitted/watched lock-free.
func (w *Watcher) reconcile(ctx context.Context, watcher *fsnotify.Watcher) {
	entries, err := os.ReadDir(w.root)
	if err != nil {
		return
	}
	// One pass over root's entries, handling its own transcript files inline
	// rather than only descending into subdirectories: a flat layout —
	// kiro-cli keeps <uuid>.jsonl directly under sessions/cli with no project
	// directory at all — would otherwise gain nothing from this sweep. This
	// deliberately does not sort via newestFirst: see its doc comment, the
	// ordering earns nothing here and costs an lstat per directory per sweep.
	for _, e := range entries {
		path := filepath.Join(w.root, e.Name())
		if !e.IsDir() {
			w.reconcileFile(path)
			continue
		}
		if ctx.Err() != nil {
			return
		}
		_ = filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return nil // skip unreadable dirs
			}
			if d.IsDir() {
				w.addWatch(watcher, p)
				return nil
			}
			w.reconcileFile(p)
			return nil
		})
		w.drainPendingEvents(watcher)
	}
}

// reconcileFile emits the event fsnotify should have delivered for path, if
// any. It mirrors handleTranscriptCreate/handleTranscriptWrite's rules — same
// session-ID extraction, same staleness cutoff, same zero-byte deferral for
// header-linked adapters — so a file recovered by the sweep is indistinguishable
// from one reported normally.
// The filters run cheapest-first, and that ordering is load-bearing rather
// than cosmetic. A stat plus two map lookups is a syscall; idFor can be far
// more than that — codex derives its session ID by opening the transcript and
// JSON-decoding its first record (codex.sessionIDFromPath), so hoisting it
// above the size compare made every sweep re-read every transcript in the tree
// to produce nothing. Measured on a real 301-file codex tree that is ~250ms
// per sweep versus ~3ms, spent on the goroutine that also dispatches fsnotify
// events — the sweep would have been delaying the fast path it exists to back
// up. Only files with a valid session ID are ever recorded in emitted, so
// consulting it before idFor cannot mask a file idFor would have skipped.
func (w *Watcher) reconcileFile(path string) {
	if !strings.HasSuffix(path, transcriptExt) {
		return
	}
	size, mtime := fileSizeAndMtime(path)
	if w.isStale(mtime) {
		return
	}
	prev, known := w.emitted[path]
	if known && prev == size {
		return // fsnotify already reported this exact state
	}
	sessionID := w.idFor(path)
	if sessionID == "" {
		return
	}
	projectDir := filepath.Base(filepath.Dir(path))
	switch {
	case known:
		w.emit(agent.EventActivity, sessionID, projectDir, path, size)
	case size == 0 && w.parentSessionID != nil:
		// Same deferral as a zero-byte Create: park it so the header is
		// readable before the new-session event goes out.
		if w.pendingNew == nil {
			w.pendingNew = make(map[string]struct{})
		}
		w.pendingNew[path] = struct{}{}
	default:
		delete(w.pendingNew, path)
		w.emit(agent.EventNewSession, sessionID, projectDir, path, size)
	}
}

// --- helpers ----------------------------------------------------------------

// extractSessionID returns the UUID session ID from a .jsonl filename.
// Returns "" if the filename doesn't match the expected pattern.
func extractSessionID(path string) string {
	base := filepath.Base(path)
	if !strings.HasSuffix(base, transcriptExt) {
		return ""
	}
	return strings.TrimSuffix(base, transcriptExt)
}

// fileSizeAndMtime returns the size and modification time of a file.
// On stat failure it returns (0, zero time).
func fileSizeAndMtime(path string) (int64, time.Time) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, time.Time{}
	}
	return info.Size(), info.ModTime()
}
