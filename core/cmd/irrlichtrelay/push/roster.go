package push

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
)

// rosterFilename persists the daemon roster
// (docs/mobile-notifications-arc42.md §8.6): every daemon the relay has seen,
// with the workspace it connected under and when. It exists for exactly one
// reader — the observer's startup seeding — so the §6.4 watchdog cannot
// silently forget a daemon that is offline while the relay restarts.
const rosterFilename = "daemon-roster.json"

// RosterEntry is one known daemon. Workspace + DaemonID identify it; Label
// and LastSeen are what the watchdog push and the operator get to see.
type RosterEntry struct {
	Workspace string `json:"workspace"`
	DaemonID  string `json:"daemon_id"`
	Label     string `json:"label"`
	LastSeen  int64  `json:"last_seen"`
}

// rosterKey identifies a roster entry: the workspace is part of the identity
// because daemon ids are only unique within their tenant.
type rosterKey struct {
	workspace string
	daemonID  string
}

// rosterFile is the on-disk shape, versioned for forward evolution and
// sorted for stable diffs.
type rosterFile struct {
	Version int           `json:"version"`
	Daemons []RosterEntry `json:"daemons"`
}

// RosterUpsert records that a daemon was seen (on connect and on disconnect
// alike — both prove the link existed just now). The file is rewritten only
// when the entry actually changed, so a repeated upsert with identical
// values costs no I/O. A write error is logged, not returned: the in-memory
// entry is already current, and the next changing upsert rewrites the file.
func (s *Service) RosterUpsert(workspace, id, label string, lastSeen int64) {
	if id == "" {
		return
	}
	entry := RosterEntry{Workspace: workspace, DaemonID: id, Label: label, LastSeen: lastSeen}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := rosterKey{workspace: workspace, daemonID: id}
	if prev, ok := s.roster[key]; ok && prev == entry {
		return
	}
	s.roster[key] = entry
	if err := s.saveRosterLocked(); err != nil {
		log.Printf("push: writing %s: %v", rosterFilename, err)
	}
}

// Roster returns a copy of the known daemons, sorted by workspace then
// daemon id — the same order the file uses.
func (s *Service) Roster() []RosterEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sortedRosterLocked()
}

// sortedRosterLocked snapshots the roster in stable order. Caller holds mu.
func (s *Service) sortedRosterLocked() []RosterEntry {
	out := make([]RosterEntry, 0, len(s.roster))
	for _, e := range s.roster {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Workspace != out[j].Workspace {
			return out[i].Workspace < out[j].Workspace
		}
		return out[i].DaemonID < out[j].DaemonID
	})
	return out
}

// saveRosterLocked rewrites the roster file. Caller holds mu.
func (s *Service) saveRosterLocked() error {
	data, err := json.MarshalIndent(rosterFile{Version: 1, Daemons: s.sortedRosterLocked()}, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(s.dir, rosterFilename), data, 0o600)
}

// loadRoster reads the persisted roster; a missing file is an empty roster,
// a corrupt one is an error naming the path.
func (s *Service) loadRoster() error {
	path := filepath.Join(s.dir, rosterFilename)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading daemon roster %s: %w", path, err)
	}
	var f rosterFile
	if err := json.Unmarshal(data, &f); err != nil {
		return fmt.Errorf("daemon roster %s is corrupt: %w — safe to delete: it is rebuilt as daemons reconnect, at the cost of the watchdog forgetting any daemon that is offline right now", path, err)
	}
	for _, e := range f.Daemons {
		s.roster[rosterKey{workspace: e.Workspace, daemonID: e.DaemonID}] = e
	}
	return nil
}
