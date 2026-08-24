package filesystem

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

const appSupportDir = "Library/Application Support/Irrlicht"

// SessionRepository implements ports/outbound.SessionRepository using the local filesystem.
type SessionRepository struct {
	instancesDir string
	// warnedStates records which unrecognised state values ListAll has already
	// logged, so a forward-compatible skip does not turn the daemon's poll loop
	// into a log flood (see warnUnknownStateOnce). A sync.Map because ListAll is
	// reachable from more than one goroutine through CachedRepository.
	warnedStates sync.Map
	// logger, when set, receives the unrecognised-state warning. It has to be
	// the Logger PORT and not stdlib `log`, because in the shipped product the
	// macOS app spawns irrlichd with both stdout and stderr pointed at
	// FileHandle.nullDevice (DaemonManager.spawnDaemon) — a `log.Printf` there
	// reaches nobody, and this warning is the ONLY signal that a session
	// silently vanished from the list. Set once at startup via SetLogger; nil
	// for the CLI paths (--diagnose, irrlicht-ls), which keep the stderr
	// fallback because their stderr is a terminal.
	logger outbound.Logger
}

// SetLogger attaches the daemon's structured logger. Safe to leave unset —
// warnUnknownStateOnce falls back to stderr. Call it before the repository is
// shared with other goroutines (startup wiring does).
func (r *SessionRepository) SetLogger(l outbound.Logger) { r.logger = l }

// New returns a SessionRepository rooted at the user's Application Support directory.
func New() (*SessionRepository, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("cannot determine home directory: %w", err)
	}
	return &SessionRepository{
		instancesDir: filepath.Join(homeDir, appSupportDir, "instances"),
	}, nil
}

// NewWithDir returns a SessionRepository rooted at the given directory (useful for tests).
func NewWithDir(dir string) *SessionRepository {
	return &SessionRepository{instancesDir: dir}
}

// InstancesDir returns the directory where session files are stored.
func (r *SessionRepository) InstancesDir() string {
	return r.instancesDir
}

// Load reads a session state from disk. Returns (nil, err) if the file does not exist.
func (r *SessionRepository) Load(sessionID string) (*session.SessionState, error) {
	path := r.statePath(sessionID)
	// statePath already routes sessionID through sanitizeSessionID, so this is
	// a no-op for any legitimate caller — but CodeQL's go/path-injection query
	// doesn't credit a sanitizer applied inside a called function one hop back
	// (see sanitizeSessionID's doc comment); checking the exact value reaching
	// os.ReadFile, right here, is what it recognizes.
	if strings.Contains(path, "..") {
		return nil, fmt.Errorf("invalid session id %q", sessionID)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var state session.SessionState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

// Save atomically writes a session state to disk.
func (r *SessionRepository) Save(state *session.SessionState) error {
	if err := os.MkdirAll(r.instancesDir, 0700); err != nil {
		return fmt.Errorf("failed to create instances directory: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal session state: %w", err)
	}
	path := r.statePath(state.SessionID)
	tmpPath := fmt.Sprintf("%s.tmp.%d.%d", path, os.Getpid(), time.Now().UnixNano())
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return fmt.Errorf("failed to write temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to rename temp file: %w", err)
	}
	return nil
}

// Delete removes a session state file. Returns nil if the file does not exist.
func (r *SessionRepository) Delete(sessionID string) error {
	err := os.Remove(r.statePath(sessionID))
	if err != nil && os.IsNotExist(err) {
		return nil
	}
	return err
}

// ListAll returns all session states found in the instances directory.
// Files that cannot be parsed are silently skipped.
func (r *SessionRepository) ListAll() ([]*session.SessionState, error) {
	entries, err := os.ReadDir(r.instancesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var states []*session.SessionState
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if filepath.Ext(name) != ".json" || strings.Contains(name, ".tmp.") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(r.instancesDir, name))
		if err != nil {
			continue
		}
		var state session.SessionState
		if err := json.Unmarshal(data, &state); err != nil {
			continue
		}
		if !session.IsCanonicalState(state.State) {
			// #1797: an unrecognised state is a value THIS build does not
			// understand — a session written by a newer daemon, read after a
			// downgrade or in a mixed-version install. It is not junk, and it
			// is never a licence to delete the user's data: this used to
			// os.Remove the file, which destroyed every such session
			// irrecoverably on the first sweep.
			//
			// What a three-state build does with it, decided here: keep the
			// file untouched and skip the session. Handing it downstream is
			// not an option — grouping, the state counters and the state
			// machine are all written against exactly working/waiting/ready,
			// so an unknown value would surface as a mis-rendered session
			// rather than an absent one.
			//
			// Two consequences of skipping, stated rather than implied:
			//   - PruneStale iterates ListAll, so these files are exempt from
			//     MaxSessionAge and accumulate until a build that understands
			//     the state reaps them. Deliberate: unbounded-but-intact beats
			//     the data loss this block exists to stop, and age-based
			//     reaping of states we cannot read is its own decision.
			//   - Load() does NOT filter by state, so a session this daemon
			//     still receives events for is loaded normally and the next
			//     Save() rewrites the state to a canonical one. "Kept for the
			//     build that understands it" therefore holds for sessions this
			//     daemon never sees again — not for live ones.
			r.warnUnknownStateOnce(state.State, name)
			continue
		}
		states = append(states, &state)
	}
	return states, nil
}

// warnUnknownStateOnce logs an unrecognised session state the first time this
// repository sees that value, and stays quiet for every later sighting.
// ListAll runs on the daemon's poll loop, so logging per occurrence would emit
// the same line every few seconds for as long as the file exists — which
// buries the one sighting that carries information (a session state this build
// has never heard of) under thousands of duplicates.
func (r *SessionRepository) warnUnknownStateOnce(state, name string) {
	if _, seen := r.warnedStates.LoadOrStore(state, struct{}{}); seen {
		return
	}
	msg := fmt.Sprintf("session file %q has unrecognized state %q (this build knows only %s/%s/%s) "+
		"— keeping the file on disk and skipping the session",
		name, state, session.StateWorking, session.StateWaiting, session.StateReady)
	if r.logger != nil {
		r.logger.LogError("session_state_unrecognized", "", msg)
		return
	}
	log.Printf("filesystem repository: %s", msg)
}

// PruneStale deletes session files older than maxAge and returns the count.
// A zero or negative maxAge disables pruning (returns 0, nil).
func (r *SessionRepository) PruneStale(maxAge time.Duration) (int, error) {
	if maxAge <= 0 {
		return 0, nil
	}
	states, err := r.ListAll()
	if err != nil {
		return 0, err
	}
	pruned := 0
	for _, s := range states {
		if s.IsStale(maxAge) {
			_ = r.Delete(s.SessionID)
			pruned++
		}
	}
	return pruned, nil
}

// statePath is the single choke point Load/Save/Delete funnel through for
// the on-disk state file location. sessionID is the daemon's own generated
// id in normal operation, but Load/Delete are reachable from the daemon's
// loopback control API (POST /api/v1/sessions/{id}/input, .../interrupt)
// with only an empty-string check on the path segment before it gets here —
// sanitizeSessionID is the defense-in-depth backstop against a "../"-style
// id escaping instancesDir.
func (r *SessionRepository) statePath(sessionID string) string {
	return filepath.Join(r.instancesDir, sanitizeSessionID(sessionID)+".json")
}

// sanitizeSessionID reduces sessionID to a single safe path segment: "" if
// it's empty, ".", "..", or contains a path separator after taking its
// final element (filepath.Base("..") returns ".." unchanged, so that case
// needs its own check). A caller that gets "" back simply misses on disk —
// the same not-found outcome as any other unrecognized session id.
func sanitizeSessionID(sessionID string) string {
	sessionID = filepath.Base(sessionID)
	if sessionID == "." || sessionID == ".." {
		return ""
	}
	return sessionID
}
