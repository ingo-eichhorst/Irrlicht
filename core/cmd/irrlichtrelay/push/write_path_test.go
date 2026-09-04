package push

// What the persisted-write path owes its callers: it must not hold the
// Service mutex while the disk is busy, and reordering two writers must not
// let an older snapshot land on a newer one.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// blockingWriter is a disk that stops on command: every write reports
// itself on entered and waits for release.
type blockingWriter struct {
	entered     chan struct{}
	release     chan struct{}
	releaseOnce sync.Once
	inner       func(path string, data []byte, perm os.FileMode) error
}

func (w *blockingWriter) write(path string, data []byte, perm os.FileMode) error {
	w.entered <- struct{}{}
	<-w.release
	return w.inner(path, data, perm)
}

// unblock lets every parked and future write through; safe to call twice so
// a test and its cleanup can both reach for it.
func (w *blockingWriter) unblock() { w.releaseOnce.Do(func() { close(w.release) }) }

// TestRosterUpsertDoesNotHoldTheLockAcrossTheWrite: one mutex guards the
// subscription registry, the daemon roster and the delivery-health map
// (docs/mobile-notifications-arc42.md §8.6 keeps all of it in one small
// Service). RosterUpsert runs on the observer goroutine, on every daemon
// connect and disconnect, and a write held under that mutex stalls every
// in-flight send's health record, every /api/v1/push/subscriptions request
// and the pairing flow's SetSubscription for as long as the disk takes —
// and stalls observation itself, which is the one thing the observer's hook
// contract spends its design on.
func TestRosterUpsertDoesNotHoldTheLockAcrossTheWrite(t *testing.T) {
	svc, clk, _ := newTestService(t)
	disk := &blockingWriter{
		entered: make(chan struct{}, 4),
		release: make(chan struct{}),
		inner:   writeFileAtomic,
	}
	svc.writeFile = disk.write
	t.Cleanup(disk.unblock)

	upserted := make(chan struct{})
	go func() {
		svc.RosterUpsert("acme", "mac-1", "laptop", clk.now().Unix())
		close(upserted)
	}()
	select {
	case <-disk.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("RosterUpsert never reached the disk")
	}

	// The disk is now parked mid-write. Every other reader of the Service
	// must still answer.
	answered := make(chan struct{})
	go func() {
		svc.Roster()
		svc.Subscriptions()
		svc.SetDeliveryStatus("token-1", DeliveryStatus{At: clk.now().Unix(), OK: true})
		close(answered)
	}()
	select {
	case <-answered:
	case <-time.After(2 * time.Second):
		t.Fatal("the Service mutex is held across the roster write — reads and delivery-health records block on the disk")
	}

	disk.unblock()
	select {
	case <-upserted:
	case <-time.After(10 * time.Second):
		t.Fatal("RosterUpsert never returned after the disk was released")
	}
}

// rosterSnapshotJSON marshals an arbitrary roster the way the Service does,
// so a test can hand the writer two snapshots without racing to produce
// them.
func rosterSnapshotJSON(t *testing.T, entries ...RosterEntry) []byte {
	t.Helper()
	data, err := json.MarshalIndent(rosterFile{Version: 1, Daemons: entries}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestAStaleRosterSnapshotNeverOverwritesAFresherOne: moving the write off
// the lock is what unblocks the readers above, and it buys a failure that
// did not exist while the write was serialized by mu — two writers can
// reach the disk in the opposite order to the one they took their snapshots
// in, and the loser's older roster lands last. The relay would then start
// from a roster missing a daemon it had already recorded, and the §6.4
// watchdog would never report that machine.
func TestAStaleRosterSnapshotNeverOverwritesAFresherOne(t *testing.T) {
	svc, clk, dir := newTestService(t)
	at := clk.now().Unix()
	path := filepath.Join(dir, rosterFilename)

	fresh := rosterSnapshotJSON(t,
		RosterEntry{Workspace: "acme", DaemonID: "alpha", Label: "desk", LastSeen: at},
		RosterEntry{Workspace: "acme", DaemonID: "mac-1", Label: "laptop", LastSeen: at},
	)
	stale := rosterSnapshotJSON(t,
		RosterEntry{Workspace: "acme", DaemonID: "mac-1", Label: "laptop", LastSeen: at},
	)

	// The fresher snapshot (seq 2) reaches the disk first; the older one
	// (seq 1) arrives after and must decline.
	if err := svc.saveRoster(fresh, 2); err != nil {
		t.Fatalf("writing the fresh snapshot: %v", err)
	}
	if err := svc.saveRoster(stale, 1); err != nil {
		t.Fatalf("writing the stale snapshot: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f rosterFile
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("roster is not JSON: %v (%s)", err, data)
	}
	if len(f.Daemons) != 2 {
		t.Fatalf("roster holds %d daemon(s) %+v — an older snapshot overwrote a newer one", len(f.Daemons), f.Daemons)
	}

	// Vacuity guard: the writer is not simply refusing everything after its
	// first write. A genuinely newer snapshot still lands.
	newest := rosterSnapshotJSON(t,
		RosterEntry{Workspace: "acme", DaemonID: "alpha", Label: "desk", LastSeen: at},
		RosterEntry{Workspace: "acme", DaemonID: "beta", Label: "mini", LastSeen: at},
		RosterEntry{Workspace: "acme", DaemonID: "mac-1", Label: "laptop", LastSeen: at},
	)
	if err := svc.saveRoster(newest, 3); err != nil {
		t.Fatalf("writing the newest snapshot: %v", err)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f = rosterFile{}
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Daemons) != 3 {
		t.Fatalf("roster holds %d daemon(s) %+v — the writer declined a fresher snapshot", len(f.Daemons), f.Daemons)
	}
}
