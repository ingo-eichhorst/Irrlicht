package push

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// rosterFilename persists the daemon roster
// (docs/mobile-notifications-arc42.md §8.6): every daemon the relay has seen,
// with the workspace it connected under and when. It exists for exactly one
// reader — the observer's startup seeding — so the §6.4 watchdog cannot
// silently forget a daemon that is offline while the relay restarts.
const rosterFilename = "daemon-roster.json"

// rosterMaxAge bounds how long an unseen daemon stays on the watchdog's
// roster. Every entry is replayed into the engines as up-then-down at
// startup (§6.4 seeding), so an entry that outlives the machine it names
// produces one "disconnected" banner per relay restart, forever, for a Mac
// nobody is waiting on — and the file only ever grew. 30 days is long
// enough that a laptop left at home over a holiday still earns its banner
// when the relay restarts, and short enough that a decommissioned machine
// stops producing one inside a month. Dropping is not forgetting for good:
// the daemon's next connect re-adds it.
const rosterMaxAge = 30 * 24 * time.Hour

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
// alike — both prove the link existed just now), sweeping any entry that has
// aged past rosterMaxAge on the way. The file is rewritten only when the
// entry actually changed, so a repeated upsert with identical values costs
// no I/O — which also means an aged entry can sit until the next CHANGING
// upsert; the sweep at load is what bounds it across a restart. A write
// error is logged, not returned: the in-memory entry is already current, and
// the next changing upsert rewrites the file.
//
// The snapshot is marshalled under the lock and written outside it: this
// runs on the observer goroutine, and one mutex guards the registry, the
// roster and the delivery-health map alike, so a write held under it stalls
// observation and every push-side HTTP read for as long as the disk takes.
func (s *Service) RosterUpsert(workspace, id, label string, lastSeen int64) {
	if id == "" {
		return
	}
	entry := RosterEntry{Workspace: workspace, DaemonID: id, Label: label, LastSeen: lastSeen}
	s.mu.Lock()
	key := rosterKey{workspace: workspace, daemonID: id}
	if prev, ok := s.roster[key]; ok && prev == entry {
		s.mu.Unlock()
		return
	}
	s.roster[key] = entry
	s.pruneRosterLocked(s.now())
	data, seq, err := s.rosterSnapshotLocked()
	s.mu.Unlock()

	if err != nil {
		log.Printf("push: encoding %s: %v", rosterFilename, err)
		return
	}
	if err := s.saveRoster(data, seq); err != nil {
		log.Printf("push: writing %s: %v", rosterFilename, err)
	}
}

// pruneRosterLocked drops every daemon unseen for longer than rosterMaxAge
// and reports how many went. Caller holds mu.
func (s *Service) pruneRosterLocked(now time.Time) int {
	// Unseen for exactly rosterMaxAge is already too long, matching the
	// pairing code's TTL boundary (see TestCodeExpiresAtExactlyTTL).
	cutoff := now.Add(-rosterMaxAge).Unix()
	dropped := 0
	for key, e := range s.roster {
		if e.LastSeen <= cutoff {
			delete(s.roster, key)
			dropped++
		}
	}
	return dropped
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

// rosterSnapshotLocked marshals the roster and stamps it with the sequence
// number saveRoster orders by. Caller holds mu.
func (s *Service) rosterSnapshotLocked() ([]byte, uint64, error) {
	s.rosterSeq++
	data, err := json.MarshalIndent(rosterFile{Version: 1, Daemons: s.sortedRosterLocked()}, "", "  ")
	return data, s.rosterSeq, err
}

// saveRoster carries one marshalled snapshot to disk, outside the Service
// lock and never behind a fresher one.
func (s *Service) saveRoster(data []byte, seq uint64) error {
	return s.rosterOut.write(s.writeFile, filepath.Join(s.dir, rosterFilename), data, 0o600, seq)
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
	s.mu.Lock()
	for _, e := range f.Daemons {
		s.roster[rosterKey{workspace: e.Workspace, daemonID: e.DaemonID}] = e
	}
	// Sweep before anyone reads this: the observer seeds the watchdog from
	// the roster at startup, so an entry that outlived its machine would be
	// replayed as a fresh disconnect (§6.4).
	dropped := s.pruneRosterLocked(s.now())
	var (
		snapshot []byte
		seq      uint64
	)
	if dropped > 0 {
		snapshot, seq, err = s.rosterSnapshotLocked()
	}
	s.mu.Unlock()

	if dropped == 0 {
		return nil
	}
	log.Printf("push: dropped %d daemon(s) unseen for more than %s from %s", dropped, rosterMaxAge, rosterFilename)
	// A failure to persist the sweep is not a failure to start: the roster
	// in memory is already correct, and the next changing upsert rewrites
	// the file.
	if err != nil {
		log.Printf("push: encoding %s after the age sweep: %v", rosterFilename, err)
		return nil
	}
	if err := s.saveRoster(snapshot, seq); err != nil {
		log.Printf("push: writing %s after the age sweep: %v", rosterFilename, err)
	}
	return nil
}
