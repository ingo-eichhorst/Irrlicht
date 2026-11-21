package filesystem

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"irrlicht/core/domain/session"
)

// finishedSubagentsDirName is the store's directory under the app-support
// dir (or under IRRLICHT_HOME, via the daemon's stateStoreDir).
const finishedSubagentsDirName = "finished-subagents"

const finishedSubagentsFileVersion = 1

// FinishedSubagentRetention is how long a root's records outlive the root
// itself. A record is never dropped while its root session exists, however
// long the root runs, and a root that goes away and comes back — `claude
// --continue` re-creates the same session ID — finds its records still
// there. Only a root that has been absent from the session list for this
// long loses them.
const FinishedSubagentRetention = 7 * 24 * time.Hour

// FinishedSubagentStore persists session.FinishedSubagent records (#2061) so
// a removed subagent's usage outlives its session and a daemon restart. It
// implements outbound.FinishedSubagentStore.
//
// One file per root session, holding every record under that root (its
// children, their children, ...). A removal therefore rewrites only its own
// root's file, whose size grows with that root's subagents alone, and an
// expired root is dropped by deleting one file. Measured once with a
// throwaway test on the dev machine (2026-11-21), not pinned by any test: a
// Claude Code record is about 620 bytes of JSON, and one Retain into a root
// already holding 2000 records took about 6 ms (100 records: 0.4 ms).
//
// Retain and PruneOrphans return their write errors for the caller to log; a
// record whose write failed is still served from memory until the daemon
// restarts.
type FinishedSubagentStore struct {
	mu    sync.Mutex
	dir   string
	roots map[string]*finishedRoot
	now   func() time.Time
}

// finishedRoot is one root's file.
type finishedRoot struct {
	Version int    `json:"version"`
	Root    string `json:"root"`
	// MissingSince is when PruneOrphans first found the root absent (Unix
	// seconds); 0 while it exists.
	MissingSince int64                      `json:"missing_since,omitempty"`
	Records      []session.FinishedSubagent `json:"records"`
}

// NewFinishedSubagentStore returns the store under
// ~/Library/Application Support/Irrlicht/finished-subagents.
func NewFinishedSubagentStore() (*FinishedSubagentStore, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return NewFinishedSubagentStoreWithDir(filepath.Join(home, appSupportDir, finishedSubagentsDirName)), nil
}

// NewFinishedSubagentStoreWithDir returns a store rooted at dir, loading the
// roots a previous run left there. A file it cannot read, or one written in
// another version, is skipped, not deleted.
func NewFinishedSubagentStoreWithDir(dir string) *FinishedSubagentStore {
	s := &FinishedSubagentStore{dir: dir, roots: make(map[string]*finishedRoot), now: time.Now}
	paths, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var r finishedRoot
		if json.Unmarshal(data, &r) != nil || r.Version != finishedSubagentsFileVersion || r.Root == "" {
			continue
		}
		s.roots[r.Root] = &r
	}
	return s
}

// Retain records one removed child under its root, replacing an earlier
// record for the same session, and rewrites that root's file.
func (s *FinishedSubagentStore) Retain(rec session.FinishedSubagent) error {
	if rec.SessionID == "" || rec.ParentSessionID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rootID := s.rootOfLocked(rec.ParentSessionID)
	r := s.roots[rootID]
	if r == nil {
		r = &finishedRoot{Version: finishedSubagentsFileVersion, Root: rootID}
		s.roots[rootID] = r
	}
	kept := r.Records[:0]
	for _, old := range r.Records {
		if old.SessionID != rec.SessionID {
			kept = append(kept, old)
		}
	}
	r.Records = append(kept, rec)
	return s.writeLocked(r)
}

// rootOfLocked resolves the root a record with this parent belongs to: the
// parent's own root when the parent is itself a retained record, else the
// parent.
func (s *FinishedSubagentStore) rootOfLocked(parentID string) string {
	for rootID, r := range s.roots {
		for _, rec := range r.Records {
			if rec.SessionID == parentID {
				return rootID
			}
		}
	}
	return parentID
}

// PruneOrphans drops the records of every root that has been absent for
// FinishedSubagentRetention. rootExists answers whether a root session is
// still in the session list; a root found absent starts its clock, and one
// found again stops it.
func (s *FinishedSubagentStore) PruneOrphans(rootExists func(rootSessionID string) bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().Unix()
	var errs []error
	for rootID, r := range s.roots {
		switch {
		case rootExists(rootID):
			if r.MissingSince != 0 {
				r.MissingSince = 0
				errs = append(errs, s.writeLocked(r))
			}
		case r.MissingSince == 0:
			r.MissingSince = now
			errs = append(errs, s.writeLocked(r))
		case now-r.MissingSince > int64(FinishedSubagentRetention/time.Second):
			delete(s.roots, rootID)
			if err := os.Remove(s.pathFor(rootID)); err != nil && !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// List returns every record, oldest removal first.
func (s *FinishedSubagentStore) List() []session.FinishedSubagent {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []session.FinishedSubagent
	for _, r := range s.roots {
		out = append(out, r.Records...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].FinishedAt != out[j].FinishedAt {
			return out[i].FinishedAt < out[j].FinishedAt
		}
		return out[i].SessionID < out[j].SessionID
	})
	return out
}

// pathFor names a root's file by a hash of its ID: session IDs come from
// agents, and a hash keeps any character they may hold out of the path.
func (s *FinishedSubagentStore) pathFor(rootID string) string {
	sum := sha256.Sum256([]byte(rootID))
	return filepath.Join(s.dir, hex.EncodeToString(sum[:16])+".json")
}

func (s *FinishedSubagentStore) writeLocked(r *finishedRoot) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("finished-subagent store: %w", err)
	}
	data, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("finished-subagent store: %w", err)
	}
	if err := writeFileAtomic(s.pathFor(r.Root), append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("finished-subagent store: write %s: %w", strings.TrimPrefix(s.pathFor(r.Root), s.dir+"/"), err)
	}
	return nil
}
