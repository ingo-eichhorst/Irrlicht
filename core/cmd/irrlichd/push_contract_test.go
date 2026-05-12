package main

import (
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
func TestContract_PushMessages(t *testing.T) {
	state := contracttesting.BuildFullSessionState()
	priority := int8(2)

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
		// main.go:706-711 — history snapshot push with SessionID, History, Generations.
		{
			"history_snapshot",
			outbound.PushMessage{
				Type:      outbound.PushTypeHistorySnapshot,
				SessionID: "00000000-0000-0000-0000-000000000001",
				History: map[string]string{
					"1":  "AAAA000000000000AAAA",
					"10": "BBBB000000000000BBBB",
					"60": "CCCC000000000000CCCC",
				},
				Generations: map[string]uint64{"1": 1000, "10": 100, "60": 10},
			},
		},
		// main.go:713-718 — history tick push with GranularitySec, Buckets, BucketGenerations.
		{
			"history_tick",
			outbound.PushMessage{
				Type:           outbound.PushTypeHistoryTick,
				GranularitySec: 1,
				Buckets: map[string]int8{
					"00000000-0000-0000-0000-000000000001": 2,
					"00000000-0000-0000-0000-000000000002": 1,
				},
				BucketGenerations: map[string]uint64{
					"00000000-0000-0000-0000-000000000001": 1001,
					"00000000-0000-0000-0000-000000000002": 501,
				},
			},
		},
		// main.go:720-725 — history upgrade push with SessionID, Priority.
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
			goldenPath := filepath.Join("testdata", "push", tc.name+".golden.json")
			compareContractGolden(t, got, goldenPath)
		})
	}
}
