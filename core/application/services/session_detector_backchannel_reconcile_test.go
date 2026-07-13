package services

import (
	"testing"

	"irrlicht/core/domain/backchannel"
	"irrlicht/core/domain/session"
)

// This file is the end-to-end regression for issue #997, reproducing the
// exact sequence observed in the mistral-vibe recording
// (replaydata/agents/mistral-vibe/scenarios/6-2_backchannel-observe):
//
//  1. A presession (proc-<PID>) is born ready.
//  2. A trust dialog opens; TerminalObserver's poll detects it and forces
//     the presession into Waiting.
//  3. The real session is born and PID discovery reconciles it onto the
//     same PID, retiring (deleting) the presession.
//  4. The dialog closes.
//
// Before the #997 fix, step 2's Waiting state and TerminalObserver's edge
// cache both stayed attached to the presession id, so step 3's delete threw
// them away — the real session never showed a waiting transition, and step
// 4's clearing edge fired against a row that no longer existed. This test
// wires TerminalObserver and SessionDetector together exactly as
// core/cmd/irrlichd/startup.go's setupBackchannel does (SetSessionSupersededHandler
// combining ReconcilePreSessionBackchannel + RekeySession) and asserts the
// reconciled real session carries the full waiting→working arc instead.
func TestBackchannelReconciliation_PreSessionToRealSession_Issue997(t *testing.T) {
	const presessionID = "proc-59047"
	const realSessionID = "session_20260713_113057_3a899ae1"
	const pid = 59047
	const adapter = "mistral-vibe"

	repo := newUIRepo(&session.SessionState{
		SessionID: presessionID, Adapter: adapter, State: session.StateReady, PID: pid,
	})
	reader := &uiReader{
		readable: map[string]bool{presessionID: true, realSessionID: true},
		screen:   map[string]string{presessionID: `Permission for the bash tool (touch *)`},
	}
	consent := uiConsent{map[string]bool{adapter: true}}

	d, rec := newFusionDetector(repo)
	obs := NewTerminalObserver(repo, reader, consent, func() bool { return true }, d, bcNopLog{})

	// Wire the reconciliation callback exactly as setupBackchannel does in
	// startup.go: a single registration fans out to both halves.
	d.SetSessionSupersededHandler(func(oldID, newID string) {
		d.ReconcilePreSessionBackchannel(oldID, newID)
		obs.RekeySession(oldID, newID)
	})

	// Step 1+2: the dialog is already open when the observer's first poll
	// sees the presession. Drain the resulting signal the same way Run()'s
	// event loop would (handleTerminalUISignal, off d.uiSignals).
	obs.observe(presessionID, adapter)
	d.handleTerminalUISignal(<-d.uiSignals)

	preState, err := d.repo.Load(presessionID)
	if err != nil || preState == nil || preState.State != session.StateWaiting {
		t.Fatalf("presession should be forced into waiting by the terminal-observer signal, got %+v (err=%v)", preState, err)
	}

	// Step 3: the real session is born and PID discovery reconciles it onto
	// the same PID — this is the exact path that fired in the recording
	// (cleanupStalePIDHolders), and it must carry the waiting state and the
	// observer's edge cache forward before deleting the presession.
	if err := d.repo.Save(&session.SessionState{
		SessionID: realSessionID, Adapter: adapter, State: session.StateReady,
	}); err != nil {
		t.Fatalf("seed real session: %v", err)
	}
	d.pidMgr.HandlePIDAssigned(pid, realSessionID)

	if _, err := d.repo.Load(presessionID); err == nil {
		t.Fatal("presession should have been deleted by reconciliation")
	}
	realState, err := d.repo.Load(realSessionID)
	if err != nil || realState == nil {
		t.Fatalf("real session should still exist: %v", err)
	}
	if realState.State != session.StateWaiting {
		t.Fatalf("reconciled session state = %q, want waiting — the presession's waiting state was lost on reconciliation (issue #997)", realState.State)
	}
	if !rec.hasTransitionTo(session.StateWaiting) {
		t.Error("expected a state_transition to waiting on the reconciled real session")
	}

	// Step 4: the dialog closes. Because RekeySession moved the observer's
	// edge-detection cache onto realSessionID, this poll of the LIVE id
	// correctly sees a falling edge (trust_dialog → none) instead of either
	// missing the edge entirely or targeting the now-deleted presession.
	reader.screen[realSessionID] = "back to work"
	obs.observe(realSessionID, adapter)
	d.handleTerminalUISignal(<-d.uiSignals)

	finalState, err := d.repo.Load(realSessionID)
	if err != nil || finalState == nil {
		t.Fatalf("real session should still exist after clearing: %v", err)
	}
	if finalState.State == session.StateWaiting {
		t.Fatal("reconciled session stranded in waiting — the clearing edge never reached it after reconciliation")
	}
	if !rec.hasTransitionFrom(session.StateWaiting) {
		t.Error("expected a state_transition out of waiting on the reconciled real session")
	}

	// Sanity: DetectUI itself really does recognize vibe's dialog phrasing —
	// confirms this test is exercising the same marker #988 fixed, not a
	// stale/looser string.
	if backchannel.DetectUI(`Permission for the bash tool (touch *)`) != backchannel.UIKindTrustDialog {
		t.Fatal("test fixture screen text no longer matches DetectUI's trust-dialog markers")
	}
}
