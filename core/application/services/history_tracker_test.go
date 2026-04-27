package services

import (
	"encoding/base64"
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

// decodeHistoryString unpacks a 20-char base64 string back into 60 priority
// codes. Mirrors the on-the-wire format the Swift client decodes.
func decodeHistoryString(t *testing.T, s string) [HistoryBucketCount]uint8 {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	if len(raw) != historyEncodedBytes {
		t.Fatalf("decoded length = %d, want %d", len(raw), historyEncodedBytes)
	}
	var out [HistoryBucketCount]uint8
	for i := 0; i < HistoryBucketCount; i++ {
		shift := uint((3 - i%4) * 2)
		out[i] = (raw[i/4] >> shift) & 0x3
	}
	return out
}

func TestHistoryTracker_EncodeUnknownSession(t *testing.T) {
	ht := NewHistoryTracker()
	if _, ok := ht.Encode("nope"); ok {
		t.Error("Encode for unknown session should return ok=false")
	}
}

func TestHistoryTracker_EncodeEmptyBuffer(t *testing.T) {
	ht := NewHistoryTracker()
	sid := "empty"
	// Session exists with all-no-data buffers (no transitions, no ticks yet).
	ht.sessions[sid] = newSessionBuffers()

	enc, ok := ht.Encode(sid)
	if !ok {
		t.Fatal("Encode missing for known empty session")
	}
	for _, g := range []string{"1", "10", "60"} {
		s, ok := enc.History[g]
		if !ok {
			t.Fatalf("missing granularity %s", g)
		}
		if len(s) != 20 {
			t.Errorf("granularity %s: encoded length = %d, want 20", g, len(s))
		}
		buckets := decodeHistoryString(t, s)
		for i, p := range buckets {
			if p != statePriorityNoData {
				t.Errorf("granularity %s: bucket[%d] = %d, want %d (no-data)", g, i, p, statePriorityNoData)
			}
		}
	}
}

func TestHistoryTracker_EncodePartialFillPadsFront(t *testing.T) {
	ht := NewHistoryTracker()
	sid := "partial"
	// Three sealed buckets: ready, working, waiting (matches
	// TestHistoryTracker_SnapshotOldestNewest setup).
	for _, s := range []string{"ready", "working", "waiting"} {
		ht.OnTransition(sid, s, epoch)
		ht.tick()
	}

	enc, ok := ht.Encode(sid)
	if !ok {
		t.Fatal("Encode failed")
	}
	buckets := decodeHistoryString(t, enc.History["1"])

	// Last 4 buckets: ready, working, waiting, waiting (carry-forward open
	// bucket inherits "waiting"). Front 56 are padding (no-data).
	for i := 0; i < HistoryBucketCount-4; i++ {
		if buckets[i] != statePriorityNoData {
			t.Errorf("front padding[%d] = %d, want no-data", i, buckets[i])
		}
	}
	want := []uint8{statePriorityReady, statePriorityWorking, statePriorityWaiting, statePriorityWaiting}
	for i, w := range want {
		if got := buckets[HistoryBucketCount-4+i]; got != w {
			t.Errorf("buckets[%d] = %d, want %d", HistoryBucketCount-4+i, got, w)
		}
	}
}

func TestHistoryTracker_EncodeFullRing(t *testing.T) {
	ht := NewHistoryTracker()
	sid := "full"
	// Fill all 60 buckets with "working".
	ht.OnTransition(sid, "working", epoch)
	for i := 0; i < HistoryBucketCount; i++ {
		ht.tick()
	}

	enc, ok := ht.Encode(sid)
	if !ok {
		t.Fatal("Encode failed")
	}
	buckets := decodeHistoryString(t, enc.History["1"])
	for i, p := range buckets {
		if p != statePriorityWorking {
			t.Errorf("buckets[%d] = %d, want %d", i, p, statePriorityWorking)
		}
	}
}

func TestHistoryTracker_EncodeAll(t *testing.T) {
	ht := NewHistoryTracker()
	ht.OnTransition("a", "working", epoch)
	ht.OnTransition("b", "waiting", epoch)

	all := ht.EncodeAll()
	if len(all) != 2 {
		t.Fatalf("len(EncodeAll) = %d, want 2", len(all))
	}
	for sid, enc := range all {
		if len(enc.History) != 3 {
			t.Errorf("session %q: granularity count = %d, want 3", sid, len(enc.History))
		}
		for _, g := range []string{"1", "10", "60"} {
			if _, ok := enc.History[g]; !ok {
				t.Errorf("session %q: missing granularity %s", sid, g)
			}
		}
	}
}

func TestHistoryTracker_EncodeBitOrder(t *testing.T) {
	// Hand-craft a buffer with 4 sealed buckets {ready, working, waiting, no-data}
	// so we can assert MSB-first byte layout of the trailing byte that holds them.
	ht := NewHistoryTracker()
	sid := "bitorder"
	ht.sessions[sid] = newSessionBuffers()
	rb := ht.sessions[sid].bufs[0]
	for i := range rb.buckets {
		rb.buckets[i] = -1
	}
	rb.buckets[0] = 0 // ready
	rb.buckets[1] = 1 // working
	rb.buckets[2] = 2 // waiting
	rb.buckets[3] = -1
	rb.head = 4
	rb.size = 4
	rb.lastState = "waiting"

	enc, ok := ht.Encode(sid)
	if !ok {
		t.Fatal("Encode failed")
	}
	raw, _ := base64.StdEncoding.DecodeString(enc.History["1"])
	// encodePriorities front-pads, so these 4 buckets land at output indices
	// 56..59 = byte 14. MSB-first: (0<<6)|(1<<4)|(2<<2)|(3) = 0b00_01_10_11 = 0x1B.
	const want = byte(0x1B)
	if raw[14] != want {
		t.Errorf("trailing byte = %#x, want %#x", raw[14], want)
	}
	// Front padding bytes must be all-no-data: 4 × 0b11 = 0xFF.
	for i := 0; i < 14; i++ {
		if raw[i] != 0xFF {
			t.Errorf("padding byte[%d] = %#x, want 0xFF", i, raw[i])
		}
	}
}

func TestHistoryTracker_EmitOnTransitionAndTick(t *testing.T) {
	ht := NewHistoryTracker()
	var events []HistoryEvent
	ht.SetEmitFunc(func(ev HistoryEvent) { events = append(events, ev) })

	ht.OnTransition("a", "working", epoch)
	ht.OnTransition("b", "waiting", epoch)

	// Two upgrades emitted, in order.
	if len(events) != 2 {
		t.Fatalf("len(events) after transitions = %d, want 2", len(events))
	}
	for i, ev := range events {
		if ev.Kind != HistoryEventUpgrade {
			t.Errorf("events[%d].Kind = %d, want Upgrade", i, ev.Kind)
		}
	}
	if events[0].SessionID != "a" || events[0].Priority != statePriorityWorking {
		t.Errorf("events[0] = %+v", events[0])
	}
	if events[1].SessionID != "b" || events[1].Priority != statePriorityWaiting {
		t.Errorf("events[1] = %+v", events[1])
	}

	events = nil
	ht.tick() // 1s ring rolls; 10s/60s do not.
	if len(events) != 1 {
		t.Fatalf("len(events) after tick = %d, want 1", len(events))
	}
	if events[0].Kind != HistoryEventTick || events[0].GranularitySec != 1 {
		t.Errorf("events[0] = %+v, want Tick @ 1s", events[0])
	}
	if events[0].Buckets["a"] != statePriorityWorking || events[0].Buckets["b"] != statePriorityWaiting {
		t.Errorf("Buckets = %+v", events[0].Buckets)
	}
}

// TestHistoryTracker_GenerationsMatchBuckets locks down the dedup contract
// the clients rely on: a snapshot's per-granularity Generations equals the
// number of ticks already folded into its History buckets, and the next
// tick after that snapshot carries Generations+1. If this invariant breaks
// (e.g. tick increments outside the sb.mu critical section), clients double-
// apply ticks on connect and history bars drift one bucket per reconnect.
func TestHistoryTracker_GenerationsMatchBuckets(t *testing.T) {
	ht := NewHistoryTracker()
	sid := "gens"
	ht.OnTransition(sid, "working", epoch)

	enc, ok := ht.Encode(sid)
	if !ok {
		t.Fatal("Encode failed")
	}
	if enc.Generations["1"] != 0 || enc.Generations["10"] != 0 || enc.Generations["60"] != 0 {
		t.Errorf("pre-tick generations = %+v, want all 0", enc.Generations)
	}

	var ticks []HistoryEvent
	ht.SetEmitFunc(func(ev HistoryEvent) {
		if ev.Kind == HistoryEventTick {
			ticks = append(ticks, ev)
		}
	})

	// One tick: 1s ring rolls; 10s/60s do not.
	ht.tick()
	if len(ticks) != 1 {
		t.Fatalf("tick count = %d, want 1", len(ticks))
	}
	if got := ticks[0].BucketGenerations[sid]; got != 1 {
		t.Errorf("first tick gen = %d, want 1", got)
	}

	// Snapshot after tick: 1s gen = 1, 10s/60s gen still 0.
	enc, _ = ht.Encode(sid)
	if enc.Generations["1"] != 1 || enc.Generations["10"] != 0 || enc.Generations["60"] != 0 {
		t.Errorf("post-tick generations = %+v, want {1:1, 10:0, 60:0}", enc.Generations)
	}

	// Ten more ticks: 1s rolls 10×, 10s rolls 1×, 60s still 0.
	ticks = nil
	for i := 0; i < 10; i++ {
		ht.tick()
	}
	enc, _ = ht.Encode(sid)
	if enc.Generations["1"] != 11 || enc.Generations["10"] != 1 || enc.Generations["60"] != 0 {
		t.Errorf("after 10 ticks generations = %+v, want {1:11, 10:1, 60:0}", enc.Generations)
	}

	// The most recent 1s tick event must carry the current 1s generation,
	// so client logic of `gen <= last → skip` deduplicates exactly once.
	var lastOneSec uint64
	for _, ev := range ticks {
		if ev.GranularitySec == 1 {
			lastOneSec = ev.BucketGenerations[sid]
		}
	}
	if lastOneSec != enc.Generations["1"] {
		t.Errorf("last 1s tick gen = %d, snapshot gen = %d — must match", lastOneSec, enc.Generations["1"])
	}
}

func TestHistoryTracker_TickEmitsAllGranularitiesAt60s(t *testing.T) {
	ht := NewHistoryTracker()
	var events []HistoryEvent
	ht.SetEmitFunc(func(ev HistoryEvent) { events = append(events, ev) })
	ht.OnTransition("s", "working", epoch)
	events = nil

	// 60 ticks → 1s rolls 60×, 10s rolls 6×, 60s rolls 1×.
	for i := 0; i < 60; i++ {
		ht.tick()
	}
	var per [3]int
	for _, ev := range events {
		if ev.Kind != HistoryEventTick {
			continue
		}
		per[granularityIndex(ev.GranularitySec)]++
	}
	if per[0] != 60 || per[1] != 6 || per[2] != 1 {
		t.Errorf("tick counts (1s,10s,60s) = %v, want (60,6,1)", per)
	}
}

func TestHistoryTracker_EmitSnapshot(t *testing.T) {
	ht := NewHistoryTracker()
	var events []HistoryEvent
	ht.SetEmitFunc(func(ev HistoryEvent) { events = append(events, ev) })
	ht.OnTransition("s", "working", epoch)
	events = nil

	ht.EmitSnapshot("s")
	if len(events) != 1 || events[0].Kind != HistoryEventSnapshot {
		t.Fatalf("expected one Snapshot event, got %+v", events)
	}
	if events[0].SessionID != "s" || len(events[0].History) != 3 {
		t.Errorf("snapshot event = %+v", events[0])
	}

	// EmitSnapshot for an unknown session lazy-creates an empty entry and
	// emits an all-no-data snapshot. That's intentional: session_created
	// fires before the first state transition, but clients still need a
	// hydration message so the row's history bar renders.
	events = nil
	ht.EmitSnapshot("fresh")
	if len(events) != 1 || events[0].SessionID != "fresh" || len(events[0].History) != 3 {
		t.Errorf("unknown-session snapshot = %+v", events)
	}
	buckets := decodeHistoryString(t, events[0].History["1"])
	for i, p := range buckets {
		if p != statePriorityNoData {
			t.Errorf("fresh snapshot bucket[%d] = %d, want no-data", i, p)
		}
	}
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
