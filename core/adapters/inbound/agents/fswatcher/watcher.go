// Package fswatcher implements a generic fsnotify-based watcher for agent
// transcript files. It watches a two-level directory tree (root/<project>/<id>.jsonl)
// and emits TranscriptEvents tagged with the adapter name.
package fswatcher

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"irrlicht/core/domain/agent"
)

// transcriptExt is the file extension for agent transcript files.
const transcriptExt = ".jsonl"

// Watcher watches a directory tree for .jsonl transcript file events.
// It implements inbound.Watcher.
type Watcher struct {
	root     string         // resolved absolute path to the watched directory
	adapter  string         // adapter name set on emitted events
	identity agent.Identity // populated via WithIdentity
	maxAge   time.Duration  // ignore files older than this (0 = no limit)

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

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
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
		w.broadcast(w.eventFor(agent.EventNewSession, sessionID, projectDir, name, size))

	case ev.Op&fsnotify.Write != 0:
		size, mtime := fileSizeAndMtime(name)
		if w.isStale(mtime) {
			return
		}
		if _, pending := w.pendingNew[name]; pending {
			delete(w.pendingNew, name)
			w.broadcast(w.eventFor(agent.EventNewSession, sessionID, projectDir, name, size))
			return
		}
		w.broadcast(w.eventFor(agent.EventActivity, sessionID, projectDir, name, size))

	case ev.Op&(fsnotify.Remove|fsnotify.Rename) != 0:
		delete(w.pendingNew, name)
		w.broadcast(w.eventFor(agent.EventRemoved, sessionID, projectDir, name, 0))
	}
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
	for _, dir := range newestFirst(w.root, entries) {
		if ctx.Err() != nil {
			return nil // Watch's own select loop will observe ctx.Done() and return
		}
		_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil // skip unreadable dirs
			}
			if d.IsDir() {
				_ = watcher.Add(path)
				w.emitExistingFiles(path)
			}
			return nil
		})
		w.drainPendingEvents(watcher)
	}
	return nil
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
// by modification time descending (newest first). Non-directory entries
// (loose files directly under root — no adapter's layout uses these) are
// skipped; an entry whose mtime can't be read sorts as if it were the oldest
// rather than aborting the scan.
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
			_ = watcher.Add(path)
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
		if w.maxAge > 0 && !mtime.IsZero() && time.Since(mtime) > w.maxAge {
			continue
		}
		w.broadcast(w.eventFor(agent.EventNewSession, sessionID, projectDir, fullPath, size))
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
