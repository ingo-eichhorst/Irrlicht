package relay

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// identityFilename is the basename of the persisted relay identity, written
// under the daemon's data dir (honors IRRLICHT_HOME via the caller).
const identityFilename = "relay-identity.json"

// Identity is a daemon's stable relay identity: a minted UUID that survives
// restarts plus a human label (defaults to the hostname).
type Identity struct {
	DaemonID    string `json:"daemon_id"`
	DaemonLabel string `json:"daemon_label"`
}

// LoadOrCreateIdentity reads the daemon's relay identity from
// <dir>/relay-identity.json, minting and persisting a new one on first use so
// the daemon_id is stable across restarts (clients dedupe sessions by it). A
// write failure is returned alongside the in-memory identity so the daemon can
// still forward this run; it just won't persist the id.
func LoadOrCreateIdentity(dir string) (Identity, error) {
	path := filepath.Join(dir, identityFilename)
	if data, err := os.ReadFile(path); err == nil {
		var id Identity
		if err := json.Unmarshal(data, &id); err == nil && id.DaemonID != "" {
			if id.DaemonLabel == "" {
				id.DaemonLabel = hostnameLabel()
			}
			return id, nil
		}
	}

	id := Identity{DaemonID: newUUID(), DaemonLabel: hostnameLabel()}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return id, err
	}
	data, err := json.MarshalIndent(id, "", "  ")
	if err != nil {
		return id, err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return id, err
	}
	return id, nil
}

// hostnameLabel returns the machine hostname, falling back to a constant when
// the lookup fails so a daemon_label is never empty on the wire.
func hostnameLabel() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "irrlicht-daemon"
	}
	return h
}

// newUUID returns a random RFC-4122 v4 UUID string. Avoids a dependency on
// google/uuid (an indirect-only module dependency) for the single id we mint.
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing is catastrophic; a zero id is still unique
		// enough for a single-node v0 and never silently corrupts state.
		return "00000000-0000-4000-8000-000000000000"
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
