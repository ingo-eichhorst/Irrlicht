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

	"irrlicht/core/pkg/webpush"
)

// registryFilename persists the subscription registry
// (docs/mobile-notifications-arc42.md §8.6): endpoint + browser keys per
// phone, linked to its TokenRecord by id. Rewritten only when the set
// changes — set, delete, prune-with-changes — never per push.
const registryFilename = "push-subscriptions.json"

// Entry is one registered delivery address, keyed by the device token that
// owns it. The workspace is deliberately NOT persisted here: dispatch and
// the orphan prune resolve it live from the token store, so there is a
// single source of truth and a revoked token has no workspace at all.
type Entry struct {
	TokenID      string               `json:"token_id"`
	Created      int64                `json:"created"`
	Subscription webpush.Subscription `json:"subscription"`
}

// registryFile is the on-disk shape, versioned for forward evolution and
// sorted by token id for stable diffs.
type registryFile struct {
	Version       int     `json:"version"`
	Subscriptions []Entry `json:"subscriptions"`
}

// SetSubscription registers sub as tokenID's delivery address, replacing any
// previous one. The replacement IS the self-heal
// (docs/mobile-notifications-arc42.md §8.3): the device token is the phone's
// durable identity, the subscription only an address the OS may reissue at
// any time — identity survives, address is re-registered. The subscription
// is validated here so a malformed registration is refused at the door,
// never stored as a dud that fails at dispatch time.
func (s *Service) SetSubscription(tokenID string, sub webpush.Subscription) error {
	if err := sub.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	created := s.now().Unix()
	if prev, ok := s.subs[tokenID]; ok {
		created = prev.Created // a re-registered address keeps its pairing's age
	}
	s.subs[tokenID] = Entry{TokenID: tokenID, Created: created, Subscription: sub}
	data, seq, err := s.subsSnapshotLocked()
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return s.saveSubscriptions(data, seq)
}

// DeleteSubscription drops tokenID's entry. Idempotent, and a no-op delete
// rewrites nothing.
func (s *Service) DeleteSubscription(tokenID string) error {
	s.mu.Lock()
	if _, ok := s.subs[tokenID]; !ok {
		s.mu.Unlock()
		return nil
	}
	delete(s.subs, tokenID)
	data, seq, err := s.subsSnapshotLocked()
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return s.saveSubscriptions(data, seq)
}

// SubscriptionOf returns tokenID's registered entry, ok=false when none
// exists — the health endpoint's "registered" answer.
func (s *Service) SubscriptionOf(tokenID string) (Entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.subs[tokenID]
	return e, ok
}

// Subscriptions returns a copy of the registry, sorted by token id.
func (s *Service) Subscriptions() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sortedEntriesLocked()
}

// Prune drops entries whose device token no longer exists — the revocation
// coupling (docs/mobile-notifications-arc42.md §8.1): `token revoke
// <device>` already kills WS access with 4401, the prune drops the delivery
// address. Returns how many entries were dropped; the file is rewritten only
// when that is non-zero.
func (s *Service) Prune(valid func(tokenID string) bool) int {
	s.mu.Lock()
	dropped := 0
	for id := range s.subs {
		if !valid(id) {
			delete(s.subs, id)
			dropped++
		}
	}
	// The delivery-health record goes with the identity, not with the
	// address. It deliberately outlives the SUBSCRIPTION — a phone pruned
	// on 410 keeps its last failure visible, which is the state the §8.3
	// panel exists to explain — but a token that no longer exists can never
	// read its own record (the health endpoint authenticates with that very
	// token) and nothing will ever replace it.
	for id := range s.health {
		if !valid(id) {
			delete(s.health, id)
		}
	}
	if dropped == 0 {
		s.mu.Unlock()
		return 0
	}
	data, seq, err := s.subsSnapshotLocked()
	s.mu.Unlock()

	// The in-memory drop already happened (no push will be sent); a stale
	// file entry is re-pruned on the next change or start.
	if err != nil {
		log.Printf("push: encoding %s after prune: %v", registryFilename, err)
		return dropped
	}
	if err := s.saveSubscriptions(data, seq); err != nil {
		log.Printf("push: writing %s after prune: %v", registryFilename, err)
	}
	return dropped
}

// PruneLoop runs Prune every tick until stop closes — started beside the
// token watch in `serve`, so a revoked device token loses its delivery
// address on the same cadence its WS access dies.
func (s *Service) PruneLoop(stop <-chan struct{}, every time.Duration, valid func(tokenID string) bool) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			s.Prune(valid)
		}
	}
}

// sortedEntriesLocked snapshots the registry sorted by token id. Caller
// holds mu.
func (s *Service) sortedEntriesLocked() []Entry {
	out := make([]Entry, 0, len(s.subs))
	for _, e := range s.subs {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TokenID < out[j].TokenID })
	return out
}

// subsSnapshotLocked marshals the registry and stamps it with the sequence
// number saveSubscriptions orders by. Caller holds mu.
func (s *Service) subsSnapshotLocked() ([]byte, uint64, error) {
	s.subsSeq++
	data, err := json.MarshalIndent(registryFile{Version: 1, Subscriptions: s.sortedEntriesLocked()}, "", "  ")
	return data, s.subsSeq, err
}

// saveSubscriptions carries one marshalled snapshot to disk, outside the
// Service lock and never behind a fresher one.
func (s *Service) saveSubscriptions(data []byte, seq uint64) error {
	return s.subsOut.write(s.writeFile, filepath.Join(s.dir, registryFilename), data, 0o600, seq)
}

// loadSubscriptions reads the persisted registry; a missing file is an empty
// registry, a corrupt one is an error naming the path.
func (s *Service) loadSubscriptions() error {
	path := filepath.Join(s.dir, registryFilename)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading subscription registry %s: %w", path, err)
	}
	var f registryFile
	if err := json.Unmarshal(data, &f); err != nil {
		return fmt.Errorf("subscription registry %s is corrupt: %w — safe to delete: each phone re-subscribes with its stored device token on its next open", path, err)
	}
	for _, e := range f.Subscriptions {
		s.subs[e.TokenID] = e
	}
	return nil
}
