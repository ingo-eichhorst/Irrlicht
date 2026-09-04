package services

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
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

func TestHistoryTracker_LoadDropsSubagentEntries(t *testing.T) {
	dir := t.TempDir()
	// Subagent histories are transient — restoring them can only leak (#593:
	// 1,151 dead agent-* entries measured in a production history.json).
	payload := `{"version":1,"sessions":{"agent-a013c0a83ecd892bd":{"1":["working"]},"fe48f71f":{"1":["working"]}}}`
	if err := os.WriteFile(filepath.Join(dir, "history.json"), []byte(payload), 0600); err != nil {
		t.Fatal(err)
	}
	ht := NewHistoryTrackerWithDir(dir)
	ht.Load()
	if _, ok := ht.Snapshot("agent-a013c0a83ecd892bd", 1); ok {
		t.Error("agent-* entry should be dropped on Load")
	}
	if _, ok := ht.Snapshot("fe48f71f", 1); !ok {
		t.Error("non-subagent entry should survive Load")
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

// decodeHistoryString unpacks an 80-char base64 string back into 60 wire
// codes. Mirrors the on-the-wire format both clients decode — one byte per
// bucket since #1805.
func decodeHistoryString(t *testing.T, s string) [HistoryBucketCount]uint8 {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	if len(raw) != HistoryBucketCount {
		t.Fatalf("decoded length = %d, want %d", len(raw), HistoryBucketCount)
	}
	var out [HistoryBucketCount]uint8
	copy(out[:], raw)
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
		if len(s) != 80 {
			t.Errorf("granularity %s: encoded length = %d, want 80", g, len(s))
		}
		buckets := decodeHistoryString(t, s)
		for i, p := range buckets {
			if p != wireCodeNoData {
				t.Errorf("granularity %s: bucket[%d] = %d, want %d (no-data)", g, i, p, wireCodeNoData)
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
		if buckets[i] != wireCodeNoData {
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
	assertGenerations(t, enc, wantGenerations{Phase: "pre-tick", One: 0, Ten: 0, Sixty: 0})

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
	assertGenerations(t, enc, wantGenerations{Phase: "post-tick", One: 1, Ten: 0, Sixty: 0})

	// Ten more ticks: 1s rolls 10×, 10s rolls 1×, 60s still 0.
	ticks = nil
	for i := 0; i < 10; i++ {
		ht.tick()
	}
	enc, _ = ht.Encode(sid)
	assertGenerations(t, enc, wantGenerations{Phase: "after 10 ticks", One: 11, Ten: 1, Sixty: 0})

	// The most recent 1s tick event must carry the current 1s generation,
	// so client logic of `gen <= last → skip` deduplicates exactly once.
	lastOneSec := lastGenerationForGranularity(ticks, sid, 1)
	if lastOneSec != enc.Generations["1"] {
		t.Errorf("last 1s tick gen = %d, snapshot gen = %d — must match", lastOneSec, enc.Generations["1"])
	}
}

// wantGenerations bundles assertGenerations' expected per-granularity values
// and the phase label naming the assertion point, keeping its parameter list
// within CodeScene's argument-count limit instead of threading each value
// through individually.
type wantGenerations struct {
	Phase string
	One   uint64
	Ten   uint64
	Sixty uint64
}

// assertGenerations checks enc's per-granularity Generations against the
// expected {1s, 10s, 60s} values in want, labeling a failure with want.Phase.
func assertGenerations(t *testing.T, enc EncodedHistory, want wantGenerations) {
	t.Helper()
	if enc.Generations["1"] != want.One || enc.Generations["10"] != want.Ten || enc.Generations["60"] != want.Sixty {
		t.Errorf("%s generations = %+v, want {1:%d, 10:%d, 60:%d}", want.Phase, enc.Generations, want.One, want.Ten, want.Sixty)
	}
}

// lastGenerationForGranularity returns sessionID's BucketGenerations from the
// last tick event in ticks matching granularitySec.
func lastGenerationForGranularity(ticks []HistoryEvent, sessionID string, granularitySec int) uint64 {
	var last uint64
	for _, ev := range ticks {
		if ev.GranularitySec == granularitySec {
			last = ev.BucketGenerations[sessionID]
		}
	}
	return last
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
		if p != wireCodeNoData {
			t.Errorf("fresh snapshot bucket[%d] = %d, want no-data", i, p)
		}
	}
}

// --- #1807: states the history strip cannot encode ---------------------------

// unencodableStates are the shapes that reach statePriority without a wire
// code. `error` USED to be one of them — it was canonical (#1798) with no slot
// left in the 2-bit field — but #1805 widened a bucket to a whole byte and gave
// it code 3, so it left this list and gained TestHistoryTracker_ErrorIsEncodable
// below. What remains is the case that can never be designed away:
// "quarantined" stands in for a state a NEWER daemon wrote into history.json
// that this build has never heard of — the downgrade/mixed-version path. That
// is why #1807's preserve-don't-coerce machinery outlives the encoding that
// prompted it.
var unencodableStates = []string{"quarantined"}

func readHistoryFile(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "history.json"))
	if err != nil {
		t.Fatalf("read history.json: %v", err)
	}
	return string(b)
}

// TestHistoryTracker_LoadUnencodableStateIsNotReady is #1807's defect test.
// A history.json bucket holding a state this build cannot encode must (a) not
// restore as `ready`, (b) not reach either client as the green `ready` code,
// and (c) survive the next save() with its original value intact.
func TestHistoryTracker_LoadUnencodableStateIsNotReady(t *testing.T) {
	for _, state := range unencodableStates {
		t.Run(state, func(t *testing.T) {
			dir := t.TempDir()
			payload := `{"version":1,"sessions":{"s":{"1":["working","` + state + `","waiting"]}}}`
			if err := os.WriteFile(filepath.Join(dir, "history.json"), []byte(payload), 0600); err != nil {
				t.Fatal(err)
			}
			ht := NewHistoryTrackerWithDir(dir)
			ht.Load()

			snap, ok := ht.Snapshot("s", 1)
			if !ok {
				t.Fatal("snapshot missing after Load")
			}
			if len(snap) != 3 {
				t.Fatalf("len(snap) = %d, want 3 (%v)", len(snap), snap)
			}
			if snap[1] == "ready" {
				t.Errorf("bucket[1] = %q — an unencodable state must never restore as ready", snap[1])
			}
			if snap[1] != state {
				t.Errorf("bucket[1] = %q, want %q preserved verbatim", snap[1], state)
			}

			// On the wire it must be the no-data code, which both client
			// decoders render as a blank slot — never the code that paints green.
			enc, ok := ht.Encode("s")
			if !ok {
				t.Fatal("Encode failed")
			}
			buckets := decodeHistoryString(t, enc.History["1"])
			if got := buckets[HistoryBucketCount-2]; got != wireCodeNoData {
				t.Errorf("wire bucket = %d, want %d (no-data); %d is ready/green",
					got, wireCodeNoData, statePriorityReady)
			}

			// ...and save() must write the original value back, not a coerced one.
			ht.save()
			if raw := readHistoryFile(t, dir); !strings.Contains(raw, `"`+state+`"`) {
				t.Errorf("save() erased the unencodable bucket %q: %s", state, raw)
			}
		})
	}
}

// assertNoUpgradeEmitted fails if any event is an upgrade — an unencodable
// state has no wire code at all, so there is nothing a client could merge.
func assertNoUpgradeEmitted(t *testing.T, events []HistoryEvent) {
	t.Helper()
	for _, ev := range events {
		if ev.Kind == HistoryEventUpgrade {
			t.Errorf("unencodable transition emitted an upgrade (priority %d): %+v", ev.Priority, ev)
		}
	}
}

// assertWireBucketsNoData decodes sid's 1s strip and checks the named bucket
// indices carry the no-data code rather than a colour.
func assertWireBucketsNoData(t *testing.T, ht *HistoryTracker, sid string, idx ...int) {
	t.Helper()
	enc, ok := ht.Encode(sid)
	if !ok {
		t.Fatal("Encode failed")
	}
	buckets := decodeHistoryString(t, enc.History["1"])
	for _, i := range idx {
		if buckets[i] != wireCodeNoData {
			t.Errorf("wire bucket[%d] = %d, want %d (no-data); %d is ready/green",
				i, buckets[i], wireCodeNoData, statePriorityReady)
		}
	}
}

// assertTickBucketNoData checks the 1s tick events carry the no-data code for
// sid, and fails if no such event was observed at all — absence of a finding
// and inability to look must not read the same.
func assertTickBucketNoData(t *testing.T, events []HistoryEvent, sid string) {
	t.Helper()
	saw := false
	for _, ev := range events {
		if ev.Kind != HistoryEventTick || ev.GranularitySec != 1 {
			continue
		}
		p, ok := ev.Buckets[sid]
		if !ok {
			continue
		}
		saw = true
		if p != wireCodeNoData {
			t.Errorf("tick bucket = %d, want %d (no-data)", p, wireCodeNoData)
		}
	}
	if !saw {
		t.Fatal("no 1s tick event carried this session — the assertion never ran")
	}
}

// TestHistoryTracker_UnencodableTransitionNeverSealsReady covers #1807's live
// half: a session that transitions into an unencodable state seals its
// following buckets as no-data rather than green, and emits nothing a client
// could merge into a green bucket.
func TestHistoryTracker_UnencodableTransitionNeverSealsReady(t *testing.T) {
	ht := NewHistoryTracker()
	var events []HistoryEvent
	ht.SetEmitFunc(func(ev HistoryEvent) { events = append(events, ev) })
	sid := "unencodable"

	ht.OnTransition(sid, "quarantined", epoch)
	assertNoUpgradeEmitted(t, events)

	events = nil
	ht.tick() // seals a bucket carrying lastState == "quarantined"

	assertWireBucketsNoData(t, ht, sid, HistoryBucketCount-2, HistoryBucketCount-1)
	assertTickBucketNoData(t, events, sid)
}

// TestHistoryTracker_UnencodableIsPurgedWhenItsBucketIsReused pins the
// invariant setBucket's doc comment promises: the verbatim string is dropped
// the moment its bucket takes an encodable value, so a reused ring slot can
// never resurrect a dead state name into a live bucket — or into history.json.
func TestHistoryTracker_UnencodableIsPurgedWhenItsBucketIsReused(t *testing.T) {
	ht := NewHistoryTracker()
	sid := "reuse"
	ht.OnTransition(sid, "quarantined", epoch)
	ht.OnTransition(sid, "waiting", epoch) // outranks no-data, takes the bucket

	rb := ht.sessions[sid].bufs[0]
	if n := len(rb.unencodable); n != 0 {
		t.Errorf("unencodable retained %d entr(ies) after the bucket was reused: %v", n, rb.unencodable)
	}
	snap, ok := ht.Snapshot(sid, 1)
	if !ok {
		t.Fatal("snapshot missing")
	}
	if got := snap[len(snap)-1]; got != "waiting" {
		t.Errorf("open bucket = %q, want waiting", got)
	}

	// A full wrap must drain the map rather than accumulate one entry per slot.
	ht.OnTransition(sid, "quarantined", epoch)
	for i := 0; i < HistoryBucketCount; i++ {
		ht.tick()
	}
	ht.OnTransition(sid, "working", epoch)
	for i := 0; i < HistoryBucketCount; i++ {
		ht.tick()
	}
	if n := len(rb.unencodable); n != 0 {
		t.Errorf("unencodable held %d entr(ies) after a full wrap of encodable ticks", n)
	}
}

// TestHistoryTracker_UnencodableSurvivesOntoABlankOpenBucket covers the tick
// phase where an unencodable transition lands on a bucket that holds no
// observed activity either. Nothing is overwritten, so the verbatim string
// must be recorded — otherwise whether a live `error` reaches history.json
// depends on when in the second it arrived.
func TestHistoryTracker_UnencodableSurvivesOntoABlankOpenBucket(t *testing.T) {
	ht := NewHistoryTracker()
	sid := "blank-open"
	ht.EmitSnapshot(sid) // creates the session with no reported state
	ht.tick()            // seals a blank bucket (lastState is still "")
	ht.OnTransition(sid, "quarantined", epoch)

	snap, ok := ht.Snapshot(sid, 1)
	if !ok {
		t.Fatal("snapshot missing")
	}
	if got := snap[len(snap)-1]; got != "quarantined" {
		t.Errorf("open bucket = %q, want %q preserved onto an otherwise blank bucket", got, "quarantined")
	}
	// Rendering is unchanged: preserved, still not painted.
	assertWireBucketsNoData(t, ht, sid, HistoryBucketCount-1)
}

// TestHistoryTracker_NoNegativePriorityReachesTheWire pins the invariant
// nothing downstream re-checks: historyEventBroadcaster copies Buckets and
// Priority field-for-field into json.Marshal, so a leaked in-memory sentinel
// would go straight out to clients.
//
// #1805 moved HALF of this invariant into the type system — the wire fields are
// uint8 now, so a negative can no longer be represented, and wireCode's
// signature enforces the conversion. The name is kept because the OTHER half is
// still only checked here: a wire value must be a ladder rung or the no-data
// sentinel, never some third thing. A vacuous bound (every uint8 is >= 0) would
// have made this test pass while checking nothing, so it asserts membership of
// the contract rather than a numeric range.
func TestHistoryTracker_NoNegativePriorityReachesTheWire(t *testing.T) {
	ht := NewHistoryTracker()
	var events []HistoryEvent
	ht.SetEmitFunc(func(ev HistoryEvent) { events = append(events, ev) })

	// A workload mixing encodable and unencodable states across every ring.
	states := []string{"working", "error", "waiting", "quarantined", "ready", ""}
	for i := 0; i < 120; i++ {
		ht.OnTransition("mixed", states[i%len(states)], epoch)
		ht.tick()
	}

	sawTick, sawUpgrade := 0, 0
	for _, ev := range events {
		sawTick += assertTickCodesInRange(t, ev)
		sawUpgrade += assertUpgradeCodeInRange(t, ev)
	}
	if sawTick == 0 || sawUpgrade == 0 {
		t.Fatalf("observed %d tick bucket(s) and %d upgrade(s) — the assertions above never ran", sawTick, sawUpgrade)
	}
}

// isWireCode reports whether c is something the wire contract allows at all:
// a ladder rung, or the no-data sentinel. Anything else — a stray sentinel, a
// value from a future rung nobody taught the clients — is a protocol break.
func isWireCode(c uint8) bool {
	return c <= statePriorityError || c == wireCodeNoData
}

// assertTickCodesInRange checks every bucket a tick event carries is a legal
// wire code, returning how many it checked. Non-tick events check nothing and
// return 0. (Snapshot events carry base64, built from the same encodePriorities
// path.)
func assertTickCodesInRange(t *testing.T, ev HistoryEvent) int {
	t.Helper()
	if ev.Kind != HistoryEventTick {
		return 0
	}
	n := 0
	for sid, p := range ev.Buckets {
		n++
		if !isWireCode(p) {
			t.Fatalf("tick bucket for %q = %d, which is neither a ladder rung (0..%d) nor no-data (%d)",
				sid, p, statePriorityError, wireCodeNoData)
		}
	}
	return n
}

// assertUpgradeCodeInRange checks an upgrade event's priority is one of the
// four encodable codes, returning 1 if it checked and 0 otherwise. No-data is
// out of range here on purpose: an upgrade to no-data is never emitted.
func assertUpgradeCodeInRange(t *testing.T, ev HistoryEvent) int {
	t.Helper()
	if ev.Kind != HistoryEventUpgrade {
		return 0
	}
	if ev.Priority > statePriorityError {
		t.Fatalf("upgrade priority = %d, outside 0..%d", ev.Priority, statePriorityError)
	}
	return 1
}

// TestHistoryTracker_UnencodableDoesNotClobberObservedActivity is a GUARD this
// change adds, not a defect test: it passes by construction both before and
// after the fix. It pins the aggregation half of #1807's decision — an
// unencodable transition LOSES the open bucket's max-merge, so the activity
// actually observed in that second survives instead of being blanked. Mutate
// bucketNoData to a value above statePriorityWaiting to see it go red.
func TestHistoryTracker_UnencodableDoesNotClobberObservedActivity(t *testing.T) {
	ht := NewHistoryTracker()
	sid := "merge"
	ht.OnTransition(sid, "working", epoch)
	ht.OnTransition(sid, "quarantined", epoch)

	snap, ok := ht.Snapshot(sid, 1)
	if !ok {
		t.Fatal("snapshot missing")
	}
	if got := snap[len(snap)-1]; got != "working" {
		t.Errorf("open bucket = %q, want working — an unencodable state must not erase observed activity", got)
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
