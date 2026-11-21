package filesystem

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"irrlicht/core/domain/session"
)

// finishedSubagentsDirName is the store's directory under the app-support
// dir (or under IRRLICHT_HOME, via the daemon's stateStoreDir).
const finishedSubagentsDirName = "finished-subagents"

const finishedSubagentsFile = "finished-subagents.json"

const finishedSubagentsFileVersion = 1

// FinishedSubagentRetention is how long a record is kept after its child
// session was removed, whatever happens to its parent. ForgetTree normally
// drops a parent's records long before that, when the parent itself is
// removed; the window bounds the records whose parent was removed by a path
// that never calls ForgetTree (a stale-file prune, a daemon that was not
// running), so the file cannot grow without bound.
const FinishedSubagentRetention = 7 * 24 * time.Hour

// FinishedSubagentStore persists session.FinishedSubagent records to one
// JSON file (#2061), so a removed subagent's usage survives a daemon restart
// as long as its parent does. It implements outbound.FinishedSubagentStore.
//
// Size: one record per removed child, usage fields only — a few hundred bytes
// each — so a session that runs hundreds of subagents holds a file of a few
// hundred kilobytes, rewritten once per removed child. Records leave with
// their root session (ForgetTree) or after FinishedSubagentRetention.
//
// A failed write is not reported: the record stays in memory and is served
// until the daemon restarts, which is the same answer the daemon gave before
// this store existed — the subagent is missing, not wrong.
type FinishedSubagentStore struct {
	mu      sync.Mutex
	path    string
	records map[string]session.FinishedSubagent
	now     func() time.Time
}

type finishedSubagentsFileShape struct {
	Version int                        `json:"version"`
	Records []session.FinishedSubagent `json:"records"`
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

// NewFinishedSubagentStoreWithDir returns a store rooted at dir, loading any
// records a previous run left there and dropping those past the retention
// window. An unreadable file starts the store empty.
func NewFinishedSubagentStoreWithDir(dir string) *FinishedSubagentStore {
	s := &FinishedSubagentStore{
		path:    filepath.Join(dir, finishedSubagentsFile),
		records: make(map[string]session.FinishedSubagent),
		now:     time.Now,
	}
	if data, err := os.ReadFile(s.path); err == nil {
		var f finishedSubagentsFileShape
		if json.Unmarshal(data, &f) == nil {
			for _, r := range f.Records {
				s.records[r.SessionID] = r
			}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pruneExpiredLocked() {
		s.persistLocked()
	}
	return s
}

// Retain records one removed child, replacing an earlier record for the same
// session.
func (s *FinishedSubagentStore) Retain(rec session.FinishedSubagent) {
	if rec.SessionID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneExpiredLocked()
	s.records[rec.SessionID] = rec
	s.persistLocked()
}

// ForgetTree drops every record descending from rootSessionID: its children,
// their children, and so on, following ParentSessionID through the records.
func (s *FinishedSubagentStore) ForgetTree(rootSessionID string) {
	if rootSessionID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	gone := map[string]bool{rootSessionID: true}
	for changed := true; changed; {
		changed = false
		for id, r := range s.records {
			if gone[r.ParentSessionID] && !gone[id] {
				gone[id] = true
				changed = true
			}
		}
	}
	removed := false
	for id := range gone {
		if _, ok := s.records[id]; ok {
			delete(s.records, id)
			removed = true
		}
	}
	if removed {
		s.persistLocked()
	}
}

// List returns every record, oldest removal first.
func (s *FinishedSubagentStore) List() []session.FinishedSubagent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sortedLocked()
}

func (s *FinishedSubagentStore) sortedLocked() []session.FinishedSubagent {
	out := make([]session.FinishedSubagent, 0, len(s.records))
	for _, r := range s.records {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].FinishedAt != out[j].FinishedAt {
			return out[i].FinishedAt < out[j].FinishedAt
		}
		return out[i].SessionID < out[j].SessionID
	})
	return out
}

// pruneExpiredLocked drops records past the retention window and reports
// whether it dropped any.
func (s *FinishedSubagentStore) pruneExpiredLocked() bool {
	cutoff := s.now().Add(-FinishedSubagentRetention).Unix()
	pruned := false
	for id, r := range s.records {
		if r.FinishedAt < cutoff {
			delete(s.records, id)
			pruned = true
		}
	}
	return pruned
}

func (s *FinishedSubagentStore) persistLocked() {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return
	}
	data, err := json.MarshalIndent(finishedSubagentsFileShape{
		Version: finishedSubagentsFileVersion,
		Records: s.sortedLocked(),
	}, "", "  ")
	if err != nil {
		return
	}
	_ = writeFileAtomic(s.path, append(data, '\n'), 0o600)
}
