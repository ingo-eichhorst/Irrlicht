package memory

import (
	"sync"
	"time"

	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// Store is an in-memory SessionRepository backed by sync.Map.
// On Load, if a session is not in memory, it falls back to disk via the fallback repository.
type Store struct {
	m        sync.Map
	fallback outbound.SessionRepository
}

// New creates a Store that falls back to the provided disk repository on Load.
func New(fallback outbound.SessionRepository) *Store {
	return &Store{fallback: fallback}
}

// Load returns the session from memory, falling back to disk if not found.
func (s *Store) Load(sessionID string) (*session.SessionState, error) {
	if v, ok := s.m.Load(sessionID); ok {
		return v.(*session.SessionState), nil
	}
	state, err := s.fallback.Load(sessionID)
	if err != nil {
		return nil, err
	}
	s.m.Store(sessionID, state)
	return state, nil
}

// Save stores the session in memory and persists it to disk.
func (s *Store) Save(state *session.SessionState) error {
	s.m.Store(state.SessionID, state)
	return s.fallback.Save(state)
}

// Delete removes the session from memory and disk.
func (s *Store) Delete(sessionID string) error {
	s.m.Delete(sessionID)
	return s.fallback.Delete(sessionID)
}

// ListAll returns all sessions currently in memory.
func (s *Store) ListAll() ([]*session.SessionState, error) {
	var states []*session.SessionState
	s.m.Range(func(_, v any) bool {
		states = append(states, v.(*session.SessionState))
		return true
	})
	return states, nil
}

// SeedFromDisk loads sessions from the disk fallback into memory, filtering
// out sessions older than maxAge. Stale instance files are deleted from disk.
// Returns the number of pruned sessions. A zero maxAge disables filtering.
func (s *Store) SeedFromDisk(maxAge time.Duration) (int, error) {
	states, err := s.fallback.ListAll()
	if err != nil {
		return 0, err
	}
	pruned := 0
	for _, state := range states {
		if state.IsStale(maxAge) {
			_ = s.fallback.Delete(state.SessionID)
			pruned++
			continue
		}
		s.m.Store(state.SessionID, state)
	}
	return pruned, nil
}
