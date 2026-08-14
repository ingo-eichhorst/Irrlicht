// Package push owns the relay's push-side state: the VAPID application
// identity, the outstanding pairing codes, and the registry of Web Push
// subscriptions. The HTTP surface in package main decides status codes and
// wires the bearer-token gate; this package decides state, and carries no
// HTTP types beyond what its API needs.
//
// A Service exists only on an auth-enabled relay — anonymous mode and push
// are mutually exclusive (docs/mobile-notifications-arc42.md §8.1): a
// push-capable relay is by definition reachable, and every push door is an
// abuse without an identity. Not constructing the service under --auth off
// also means no VAPID key material is ever created for a mode that cannot
// use it.
package push

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"irrlicht/core/pkg/webpush"
)

// vapidFilename is the relay's persisted ES256 application identity
// (docs/mobile-notifications-arc42.md §8.6). Every subscription is bound to
// its public half at subscribe time.
const vapidFilename = "vapid-keys.json"

// Service is the relay's push-side state, safe for concurrent use. The
// VAPID identity is immutable after construction; codes, redeem-failure
// timestamps, the subscription registry, the daemon roster and the delivery
// health map share one mutex.
type Service struct {
	dir string
	now func() time.Time

	vapid *webpush.VAPIDKey

	mu       sync.Mutex
	codes    []pairingCode
	failures []time.Time               // failed Redeem timestamps within the rolling window
	subs     map[string]Entry          // device token id → registered subscription
	roster   map[rosterKey]RosterEntry // known daemons for the §6.4 watchdog
	health   map[string]DeliveryStatus // device token id → last send outcome (RAM only, §8.6)
}

// NewService loads (or on first run creates) the push state under dir. now
// is the injected clock for pairing-code TTLs and the redeem rate limit;
// nil means time.Now.
func NewService(dir string, now func() time.Time) (*Service, error) {
	if now == nil {
		now = time.Now
	}
	s := &Service{
		dir:    dir,
		now:    now,
		subs:   map[string]Entry{},
		roster: map[rosterKey]RosterEntry{},
		health: map[string]DeliveryStatus{},
	}
	if err := s.loadOrCreateVAPID(); err != nil {
		return nil, err
	}
	if err := s.loadSubscriptions(); err != nil {
		return nil, err
	}
	if err := s.loadRoster(); err != nil {
		return nil, err
	}
	return s, nil
}

// loadOrCreateVAPID reads the persisted VAPID identity, minting and writing
// one on first run. A file that exists but does not load is a returned
// error, never a regeneration: subscriptions are bound to the public half,
// so silently minting a fresh key would orphan every paired phone. A LOST
// file self-heals — each PWA re-subscribes with the new key on its next
// open (docs/mobile-notifications-arc42.md §8.6) — but a corrupt one must
// be a human decision, which is why the caller fatals naming the file.
func (s *Service) loadOrCreateVAPID() error {
	path := filepath.Join(s.dir, vapidFilename)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		key, err := webpush.GenerateVAPIDKey()
		if err != nil {
			return err
		}
		out, err := json.Marshal(key)
		if err != nil {
			return err
		}
		if err := writeFileAtomic(path, out, 0o600); err != nil {
			return err
		}
		s.vapid = key
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading VAPID identity %s: %w", path, err)
	}
	key := &webpush.VAPIDKey{}
	if err := json.Unmarshal(data, key); err != nil {
		return fmt.Errorf("VAPID identity %s is corrupt: %w — not regenerating: every subscription is bound to this identity; restore it from backup, or delete it to accept re-pairing every phone", path, err)
	}
	s.vapid = key
	return nil
}

// VAPIDPublicKey returns the base64url applicationServerKey browsers pass to
// pushManager.subscribe(). Public by construction — it rides in every
// subscribe() call — which is why the feature-detection endpoint may serve
// it unauthenticated.
func (s *Service) VAPIDPublicKey() string {
	return s.vapid.PublicKey()
}

// VAPIDKey returns the relay's signing identity for the dispatch path — the
// key the production webpush.Sender signs every POST with. Immutable after
// construction, so handing it out needs no lock.
func (s *Service) VAPIDKey() *webpush.VAPIDKey {
	return s.vapid
}

// writeFileAtomic writes data at perm via a same-directory temp file and
// rename, so a crash mid-write can never leave a truncated file — both push
// files are load-bearing state, kept in the relay's existing shape: flat
// JSON, 0600, atomic temp+rename (docs/mobile-notifications-arc42.md §8.6).
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
