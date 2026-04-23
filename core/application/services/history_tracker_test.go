package services

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

var epoch = time.Unix(0, 0)

func TestHistoryTracker_PriorityAggregation(t *testing.T) {
	ht := NewHistoryTracker()
	sid := "test-session"

	// Within a single bucket: waiting overrides working overrides ready.
	ht.OnTransition(sid, "ready", epoch)
	ht.OnTransition(sid, "working", epoch)
	ht.OnTransition(sid, "waiting", epoch)
	ht.OnTransition(sid, "working", epoch)

	snap, ok := ht.Snapshot(sid, 1)
	if !ok {
		t.Fatal("snapshot not found")
	}
	if len(snap) < 1 {
		t.Fatal("snapshot empty")
	}
	last := snap[len(snap)-1]
	if last != "waiting" {
		t.Errorf("expected waiting, got %q", last)
	}
}

func TestHistoryTracker_BucketRollover(t *testing.T) {
	ht := NewHistoryTracker()
	sid := "rollover"

	ht.OnTransition(sid, "working", epoch)

	// Advance past one 1-second bucket.
	ht.tick()

	// Now a fresh bucket is open, seeded with carry-forward ("working").
	// Another transition should affect only the new bucket.
	ht.OnTransition(sid, "ready", epoch)

	snap, ok := ht.Snapshot(sid, 1)
	if !ok {
		t.Fatal("snapshot not found")
	}
	if len(snap) < 2 {
		t.Fatalf("expected ≥2 buckets, got %d", len(snap))
	}
	// First bucket: working
	if snap[0] != "working" {
		t.Errorf("bucket[0] = %q, want working", snap[0])
	}
	// Second bucket started as carry-forward "working" but then received "ready"
	// which has lower priority — so it stays "working".
	if snap[1] != "working" {
		t.Errorf("bucket[1] = %q, want working (carry-forward wins)", snap[1])
	}
}

func TestHistoryTracker_SnapshotOldestNewest(t *testing.T) {
	ht := NewHistoryTracker()
	sid := "order"

	// Transition, then tick to seal each bucket before the next transition.
	// Bucket 0: "ready" (only transition), sealed by tick.
	// Bucket 1: starts with carry-forward "ready", then "working" upgrades it → "working", sealed.
	// Bucket 2: starts with carry-forward "working", then "waiting" upgrades it → "waiting", open.
	for _, s := range []string{"ready", "working", "waiting"} {
		ht.OnTransition(sid, s, epoch)
		ht.tick()
	}

	snap, ok := ht.Snapshot(sid, 1)
	if !ok {
		t.Fatal("snapshot not found")
	}
	if len(snap) < 3 {
		t.Fatalf("expected ≥3 entries, got %d", len(snap))
	}

	// oldest → newest: ready, working, waiting
	want := []string{"ready", "working", "waiting"}
	for i, w := range want {
		if snap[i] != w {
			t.Errorf("snap[%d] = %q, want %q", i, snap[i], w)
		}
	}
}

func TestHistoryTracker_Remove(t *testing.T) {
	ht := NewHistoryTracker()
	sid := "to-remove"

	ht.OnTransition(sid, "working", epoch)
	ht.Remove(sid)

	_, ok := ht.Snapshot(sid, 1)
	if ok {
		t.Error("expected snapshot to be gone after Remove")
	}
}

func TestHistoryTracker_GranularityVariants(t *testing.T) {
	ht := NewHistoryTracker()
	sid := "gran"

	ht.OnTransition(sid, "waiting", epoch)

	for _, g := range []int{1, 10, 60} {
		snap, ok := ht.Snapshot(sid, g)
		if !ok {
			t.Errorf("granularity %d: snapshot not found", g)
			continue
		}
		if len(snap) == 0 {
			t.Errorf("granularity %d: empty snapshot", g)
			continue
		}
		if snap[len(snap)-1] != "waiting" {
			t.Errorf("granularity %d: got %q, want waiting", g, snap[len(snap)-1])
		}
	}
}

func TestHistoryTracker_SaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	ht := NewHistoryTrackerWithDir(dir)
	sid := "round-trip"

	// Seal three 1s buckets: ready, working, waiting.
	for _, s := range []string{"ready", "working", "waiting"} {
		ht.OnTransition(sid, s, epoch)
		ht.tick()
	}

	ht.save()

	// history.json exists on disk.
	if _, err := os.Stat(filepath.Join(dir, "history.json")); err != nil {
		t.Fatalf("history.json not written: %v", err)
	}

	// Fresh tracker pointed at same dir reconstructs identical 1s snapshot.
	ht2 := NewHistoryTrackerWithDir(dir)
	ht2.Load()

	snap, ok := ht2.Snapshot(sid, 1)
	if !ok {
		t.Fatal("snapshot missing after Load")
	}
	// Each transition+tick seals one bucket, then a trailing carry-forward
	// bucket is opened — same shape as TestHistoryTracker_SnapshotOldestNewest.
	want := []string{"ready", "working", "waiting"}
	if len(snap) < len(want) {
		t.Fatalf("len(snap) = %d, want ≥%d (%v)", len(snap), len(want), snap)
	}
	for i, w := range want {
		if snap[i] != w {
			t.Errorf("snap[%d] = %q, want %q", i, snap[i], w)
		}
	}

	// Every granularity that had data must also round-trip.
	for _, g := range []int{10, 60} {
		if _, ok := ht2.Snapshot(sid, g); !ok {
			t.Errorf("granularity %d: snapshot missing after Load", g)
		}
	}
}

func TestHistoryTracker_LoadMissingFile(t *testing.T) {
	// Empty dir — Load is silent, tracker stays empty.
	ht := NewHistoryTrackerWithDir(t.TempDir())
	ht.Load()
	if _, ok := ht.Snapshot("any", 1); ok {
		t.Error("snapshot should not exist for unseen session")
	}
}

func TestHistoryTracker_LoadCorruptFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "history.json"), []byte("not-json"), 0600); err != nil {
		t.Fatal(err)
	}
	ht := NewHistoryTrackerWithDir(dir)
	ht.Load() // must not panic
	if _, ok := ht.Snapshot("any", 1); ok {
		t.Error("corrupt file should yield empty tracker")
	}
}

func TestHistoryTracker_LoadVersionMismatch(t *testing.T) {
	dir := t.TempDir()
	// Valid JSON, but schema version we don't support.
	payload := `{"version":2,"sessions":{"s":{"1":["working"]}}}`
	if err := os.WriteFile(filepath.Join(dir, "history.json"), []byte(payload), 0600); err != nil {
		t.Fatal(err)
	}
	ht := NewHistoryTrackerWithDir(dir)
	ht.Load()
	if _, ok := ht.Snapshot("s", 1); ok {
		t.Error("v2 file should be ignored, not imported")
	}
}

func TestHistoryTracker_NoSaveWithoutDir(t *testing.T) {
	// Baseline tracker has no saveDir — save() is a silent no-op.
	ht := NewHistoryTracker()
	ht.OnTransition("s", "working", epoch)
	ht.save() // must not panic, must not create any file
}

func TestValidGranularity(t *testing.T) {
	for _, g := range []int{1, 10, 60} {
		if !validGranularity(g) {
			t.Errorf("expected %d to be valid", g)
		}
	}
	for _, g := range []int{0, 2, 5, 30, 100} {
		if validGranularity(g) {
			t.Errorf("expected %d to be invalid", g)
		}
	}
}
