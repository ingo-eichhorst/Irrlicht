package services

import (
	"bytes"
	"encoding/base64"
	"testing"

	"irrlicht/core/domain/session"
)

// #1805 — `error` on the wire.
//
// Only the material #1805 ADDED lives here. The pre-existing encode and
// unencodable-state tests stayed in history_tracker_test.go on purpose: moving
// them would have re-attributed their long-standing complexity to this change
// and made the diff read as a rewrite of tests it does not touch.

func TestHistoryTracker_EncodeByteLayout(t *testing.T) {
	// Hand-craft a buffer with 4 sealed buckets {ready, working, waiting, no-data}
	// so we can assert the byte layout of the tail that holds them.
	ht := NewHistoryTracker()
	sid := "bytelayout"
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
	if len(raw) != HistoryBucketCount {
		t.Fatalf("payload = %d bytes, want %d", len(raw), HistoryBucketCount)
	}
	// encodePriorities front-pads, so these 4 buckets land at output indices
	// 56..59 — one byte each, in order, no shifting.
	want := make([]byte, HistoryBucketCount)
	for i := range want {
		want[i] = wireCodeNoData // front padding
	}
	copy(want[HistoryBucketCount-4:], []byte{
		statePriorityReady, statePriorityWorking, statePriorityWaiting, wireCodeNoData,
	})
	if !bytes.Equal(raw, want) {
		t.Errorf("payload mismatch\n got: %v\nwant: %v", raw, want)
	}
}

// --- #1805: `error` on the wire ---------------------------------------------

// TestHistoryTracker_WireCodesAreTheLiteralsTheClientsHardcode is the only
// thing standing between these constants and two silently-wrong clients.
//
// Neither client can import them. Swift's historyPriorityToState and the web's
// HISTORY_CODE_TO_STATE hardcode 0/1/2/3 and 255 as literals, and both answer
// "" — a blank bucket — for any code they do not recognise. So moving a
// constant here does not break a build or fail a decode; it just makes every
// bucket of that state render blank on both platforms, which is precisely the
// bug #1805 exists to fix, reintroduced silently.
//
// This is a LITERAL assertion on purpose. The nearby layout and contract tests
// are written in terms of the constants, so they follow a move rather than
// catching it. Verified by mutation: changing statePriorityError to 4 leaves
// the whole services + irrlichd suite green without this test.
//
// If you change a value here, change platforms/macos/Irrlicht/Managers/
// SessionManager+History.swift and platforms/web/irrlicht.js in the same commit.
func TestHistoryTracker_WireCodesAreTheLiteralsTheClientsHardcode(t *testing.T) {
	for _, tc := range []struct {
		name string
		got  int
		want int
	}{
		{"ready", statePriorityReady, 0},
		{"working", statePriorityWorking, 1},
		{"waiting", statePriorityWaiting, 2},
		{"error", statePriorityError, 3},
		{"no-data", int(wireCodeNoData), 255},
	} {
		if tc.got != tc.want {
			t.Errorf("%s wire code = %d, want %d — update both client decoders in the same commit", tc.name, tc.got, tc.want)
		}
	}
}

// TestHistoryTracker_WireCodeMapsEveryNegativeToNoData exercises wireCode's sign
// branch with a sentinel that is NOT -1.
//
// It exists because the obvious mutation is green: wireCodeNoData (255) is
// exactly uint8(int8(-1)), so deleting the branch changes nothing for the only
// negative in the code today. -2 separates them — uint8(-2) is 254 — so this is
// the one test that can tell the guard from its absence.
func TestHistoryTracker_WireCodeMapsEveryNegativeToNoData(t *testing.T) {
	for _, p := range []int8{-1, -2, -128} {
		if got := wireCode(p); got != wireCodeNoData {
			t.Errorf("wireCode(%d) = %d, want %d (no-data)", p, got, wireCodeNoData)
		}
	}
}

// TestHistoryTracker_ErrorEmitsAnUpgrade is #1805's defect test, first half.
// Before the byte-wide encoding `error` had no code — every slot of the 2-bit
// field was spent — so a transition into it emitted nothing at all. RED-FIRST:
// delete the session.StateError arm from statePriority and this fails with "no
// upgrade emitted".
func TestHistoryTracker_ErrorEmitsAnUpgrade(t *testing.T) {
	ht := NewHistoryTracker()
	var events []HistoryEvent
	ht.SetEmitFunc(func(ev HistoryEvent) { events = append(events, ev) })
	sid := "errored"

	ht.OnTransition(sid, session.StateError, epoch)

	sawUpgrade := false
	for _, ev := range events {
		if ev.Kind != HistoryEventUpgrade || ev.SessionID != sid {
			continue
		}
		sawUpgrade = true
		if ev.Priority != statePriorityError {
			t.Errorf("upgrade priority = %d, want %d", ev.Priority, statePriorityError)
		}
	}
	if !sawUpgrade {
		t.Error("no upgrade emitted for a transition into error")
	}
}

// TestHistoryTracker_ErrorSealsOnTheWire is #1805's defect test, second half: a
// sealed bucket carries the error code rather than no-data. RED-FIRST: without
// the session.StateError arm this reports "sealed wire bucket = 255, want 3".
func TestHistoryTracker_ErrorSealsOnTheWire(t *testing.T) {
	ht := NewHistoryTracker()
	sid := "errored"
	ht.OnTransition(sid, session.StateError, epoch)
	ht.tick()

	enc, ok := ht.Encode(sid)
	if !ok {
		t.Fatal("Encode missing")
	}
	buckets := decodeHistoryString(t, enc.History["1"])
	if got := buckets[HistoryBucketCount-1]; got != statePriorityError {
		t.Errorf("sealed wire bucket = %d, want %d (error)", got, statePriorityError)
	}
}

// TestHistoryTracker_ErrorRoundTripsByName is a LOCK, not a defect test, and
// the distinction is worth the comment because it originally shipped inside
// #1805's defect test as if it were evidence.
//
// It passes with or without #1805: delete the session.StateError arm and it
// STILL passes, because #1807's ringBuffer.unencodable stores the verbatim
// state name and snapshot() reads it straight back. So it pins #1807's
// preserve-don't-coerce behaviour surviving #1805 — which is what makes errors
// recorded by an older daemon paint red immediately after the upgrade instead
// of ageing out — and proves nothing about the encoding itself.
func TestHistoryTracker_ErrorRoundTripsByName(t *testing.T) {
	ht := NewHistoryTracker()
	sid := "errored"
	ht.OnTransition(sid, session.StateError, epoch)

	snap, ok := ht.Snapshot(sid, 1)
	if !ok {
		t.Fatal("snapshot missing")
	}
	if got := snap[len(snap)-1]; got != session.StateError {
		t.Errorf("snapshot bucket = %q, want %q", got, session.StateError)
	}
}

// TestHistoryTracker_ErrorOutranksEveryOtherState pins the ladder decision
// #1805 made: error sits on top, so one error in a bucket paints the whole
// bucket red rather than being averaged away by surrounding activity. This is
// the opposite of the #1807 behaviour it replaces, where error LOST every merge
// because it had no code — so it is a genuine behaviour change, asserted here
// rather than left implicit in a constant's value.
func TestHistoryTracker_ErrorOutranksEveryOtherState(t *testing.T) {
	// Derived from session.CanonicalStates() rather than retyped, so a fifth
	// state is covered here the day it is declared — and so this test needs no
	// state-vocabulary-lint waiver (AGENTS.md: "declared once ... derive from
	// it, never retype it").
	for _, other := range session.CanonicalStates() {
		if other == session.StateError {
			continue // the state under test cannot also be the occupant
		}
		t.Run(other, func(t *testing.T) {
			// Whichever order they arrive in, the bucket ends up error.
			for _, order := range [][2]string{{other, session.StateError}, {session.StateError, other}} {
				ht := NewHistoryTracker()
				sid := "ladder"
				ht.OnTransition(sid, order[0], epoch)
				ht.OnTransition(sid, order[1], epoch)
				snap, ok := ht.Snapshot(sid, 1)
				if !ok {
					t.Fatal("snapshot missing")
				}
				if got := snap[len(snap)-1]; got != session.StateError {
					t.Errorf("%s then %s: open bucket = %q, want error", order[0], order[1], got)
				}
			}
		})
	}
}
