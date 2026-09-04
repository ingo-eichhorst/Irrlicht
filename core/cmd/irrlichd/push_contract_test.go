package main

import (
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"testing"

	"irrlicht/core/internal/contracttesting"
	"irrlicht/core/ports/outbound"
)

// TestContract_PushMessages locks the WebSocket envelope shape for every
// PushMessage type the daemon emits. The dashboard and the macOS Swift app
// both decode these envelopes; any drift in field names, presence, or
// nesting is a wire-protocol regression.
//
// Refresh every push golden with:
//
//	UPDATE_CONTRACT_GOLDENS=1 go test ./core/cmd/irrlichd/...
//
// Each case constructs the exact `outbound.PushMessage` produced by the
// corresponding emission site in the codebase (see file:line citations on
// each case). The contract is "fields populated per type" — not "exact
// bytes the WebSocket writes" — so json.MarshalIndent is used for golden
// readability.
// The history wire codes, restated here rather than imported so this test
// pins the ENVELOPE against fixed literals instead of following whatever
// services currently defines.
//
// Stated precisely, because an earlier draft of this comment claimed a guard
// this file does not provide: it does NOT fail when services' constants move.
// Nothing here imports services, so a moved constant produces the same bytes
// and stays green. The tripwire for that is
// TestHistoryTracker_WireCodesAreTheLiteralsTheClientsHardcode, in the services
// package, which asserts the literals directly. What this file pins is the JSON
// SHAPE — field names, presence, nesting and the _v2 type strings (#1805).
const (
	statePriorityReady   = 0
	statePriorityWorking = 1
	statePriorityWaiting = 2
	statePriorityError   = 3
	wireCodeNoData       = 255
)

// historySample encodes one granularity the way services.encodePriorities does
// since #1805: 60 buckets, one byte each, newest last, front-padded with the
// no-data sentinel. Built rather than hardcoded so the golden pins real bytes
// while the call site still says which states it means.
func historySample(tail ...byte) string {
	buf := make([]byte, 60)
	for i := range buf {
		buf[i] = wireCodeNoData
	}
	copy(buf[60-len(tail):], tail)
	return base64.StdEncoding.EncodeToString(buf)
}

func TestContract_PushMessages(t *testing.T) {
	state := contracttesting.BuildFullSessionState()
	// An `error` upgrade (code 3). Deliberately the state #1805 added rather
	// than a pre-existing one: it is the code an older client would have
	// misread, so it is the one worth pinning in the envelope.
	priority := uint8(3)

	cases := []struct {
		name string
		msg  outbound.PushMessage
	}{
		// session_detector_activity.go:117 — `d.broadcast(outbound.PushTypeCreated, state)`.
		{"session_created", outbound.PushMessage{Type: outbound.PushTypeCreated, Session: state}},
		// session_detector_activity.go:375 — `d.broadcast(outbound.PushTypeUpdated, state)`.
		{"session_updated", outbound.PushMessage{Type: outbound.PushTypeUpdated, Session: state}},
		// pid_manager.go (multiple sites) — `pm.broadcast(outbound.PushTypeDeleted, state)`.
		{"session_deleted", outbound.PushMessage{Type: outbound.PushTypeDeleted, Session: state}},
		// focus_service.go:43-46 — explicit Type + Session.
		{"focus_requested", outbound.PushMessage{Type: outbound.PushTypeFocusRequested, Session: state}},
		// startup.go:215 (and :248 for the connect-time provider) — history
		// snapshot push with SessionID, History, Generations.
		{
			"history_snapshot",
			outbound.PushMessage{
				Type:      outbound.PushTypeHistorySnapshot,
				SessionID: "00000000-0000-0000-0000-000000000001",
				History: map[string]string{
					"1":  historySample(statePriorityReady, statePriorityWorking, statePriorityWaiting, statePriorityError),
					"10": historySample(statePriorityWorking),
					"60": historySample(statePriorityWaiting),
				},
				Generations: map[string]uint64{"1": 1000, "10": 100, "60": 10},
			},
		},
		// startup.go:222 — history tick push with GranularitySec, Buckets, BucketGenerations.
		{
			"history_tick",
			outbound.PushMessage{
				Type:           outbound.PushTypeHistoryTick,
				GranularitySec: 1,
				Buckets: map[string]uint8{
					"00000000-0000-0000-0000-000000000001": statePriorityError,
					"00000000-0000-0000-0000-000000000002": wireCodeNoData,
				},
				BucketGenerations: map[string]uint64{
					"00000000-0000-0000-0000-000000000001": 1001,
					"00000000-0000-0000-0000-000000000002": 501,
				},
			},
		},
		// startup.go:230 — history upgrade push with SessionID, Priority.
		{
			"history_upgrade",
			outbound.PushMessage{
				Type:      outbound.PushTypeHistoryUpgrade,
				SessionID: "00000000-0000-0000-0000-000000000001",
				Priority:  &priority,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.MarshalIndent(tc.msg, "", "  ")
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			contracttesting.CompareGolden(t, got, filepath.Join("testdata", "push", tc.name+".golden.json"))
		})
	}
}
