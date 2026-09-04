package main

// The driver's own small state file: the synthetic daemon identity it
// connects with, and the device token it observes through. Both are stable
// across runs when a path is given, which is not a convenience.
//
// The relay persists a roster of every daemon it has ever seen
// (docs/mobile-notifications-arc42.md §8.6) and replays it into the watchdog
// at startup, so each distinct daemon_id this driver invents costs one
// "disconnected" banner per relay restart until the entry ages out. One
// stable id per rig means one such entry, not one per run.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// driverState is the on-disk shape. The device token is a bearer credential,
// so the file is 0600 in a 0700 directory — the same posture the relay's own
// state files hold (docs/elfdans-device-test.md Phase 1).
type driverState struct {
	DaemonID      string `json:"daemon_id"`
	DaemonLabel   string `json:"daemon_label"`
	DeviceToken   string `json:"device_token,omitempty"`
	DeviceTokenID string `json:"device_token_id,omitempty"`
	// LastRun is when this driver last drove anything, in unix seconds. The
	// burst window (§8.4) is per workspace, 20 s wide, and its entries
	// outlive the sessions that produced them, so the next run waits out
	// whatever is left of it — see env.clearBurstWindow.
	LastRun int64 `json:"last_run,omitempty"`
}

// loadState reads the state file, minting a fresh identity when there is no
// path, no file, or a file that will not parse. A missing file is the normal
// first run; an unparseable one is recoverable by construction (everything in
// it is re-derivable), so it is replaced rather than fatal.
func loadState(path string) (*driverState, error) {
	st := &driverState{}
	if path != "" {
		if data, err := os.ReadFile(path); err == nil {
			_ = json.Unmarshal(data, st)
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
	}
	if st.DaemonID == "" {
		st.DaemonID = syntheticDaemonID()
	}
	if st.DaemonLabel == "" {
		st.DaemonLabel = "elfdans-drive (synthetic)"
	}
	return st, nil
}

// saveState persists the identity and the paired device token. A run without
// a state path keeps both in memory only.
func saveState(path string, st *driverState) error {
	if path == "" {
		return nil
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("creating %s: %w", dir, err)
		}
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// syntheticDaemonID mints an id no real daemon can hold. A real one is a v4
// UUID minted by relay.LoadOrCreateIdentity; this is a prefixed hex string,
// which is unmistakable in `daemon-roster.json`, in a client's
// connection-status tooltip, and in the "Mac X disconnected" banner the
// watchdog composes from it. The relay keys cached state by (workspace,
// daemon_id, session_id), so an id that could collide with a real daemon's
// would overwrite that daemon's sessions in the relay's cache.
func syntheticDaemonID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Unique enough for a driver, and never silently indistinguishable
		// from a real id, which is the property that matters here.
		return "elfdans-drive-00000000"
	}
	return "elfdans-drive-" + hex.EncodeToString(b[:])
}
