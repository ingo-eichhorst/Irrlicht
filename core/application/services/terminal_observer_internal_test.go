package services

import (
	"errors"
	"testing"
	"time"

	"irrlicht/core/domain/backchannel"
	"irrlicht/core/domain/lifecycle"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// --- fakes shared by the observer + fusion tests ---

// uiRepo is a tiny SessionRepository: ListAll returns its sessions, Load/Save
// operate on the same backing map keyed by SessionID.
type uiRepo struct {
	sessions map[string]*session.SessionState
	saveErr  error
}

func newUIRepo(states ...*session.SessionState) *uiRepo {
	m := make(map[string]*session.SessionState, len(states))
	for _, s := range states {
		m[s.SessionID] = s
	}
	return &uiRepo{sessions: m}
}

func (r *uiRepo) Load(id string) (*session.SessionState, error) {
	s, ok := r.sessions[id]
	if !ok {
		return nil, errors.New("not found")
	}
	return s, nil
}
func (r *uiRepo) Save(s *session.SessionState) error {
	if r.saveErr != nil {
		return r.saveErr
	}
	r.sessions[s.SessionID] = s
	return nil
}
func (r *uiRepo) Delete(id string) error { delete(r.sessions, id); return nil }
func (r *uiRepo) ListAll() ([]*session.SessionState, error) {
	out := make([]*session.SessionState, 0, len(r.sessions))
	for _, s := range r.sessions {
		out = append(out, s)
	}
	return out, nil
}

// uiReader is a programmable TerminalReader.
type uiReader struct {
	readable map[string]bool
	screen   map[string]string
	err      error
}

func (r *uiReader) CaptureScreen(id string) ([]byte, error) {
	if r.err != nil {
		return nil, r.err
	}
	if !r.readable[id] {
		return nil, outbound.ErrNotReadable
	}
	return []byte(r.screen[id]), nil
}

// uiConsent is a consentGranter that grants a fixed set of adapters.
type uiConsent struct{ granted map[string]bool }

func (c uiConsent) Granted(adapter, _ string) bool { return c.granted[adapter] }

// recordingSink captures the signals the observer forwards.
type recordingSink struct {
	got []terminalUISignal
}

func (s *recordingSink) EnqueueTerminalUISignal(id string, ui backchannel.UIKind) {
	s.got = append(s.got, terminalUISignal{sessionID: id, ui: ui})
}

// --- observer (edge detection + gating) ---

func TestObserverForwardsOnlyEdges(t *testing.T) {
	st := &session.SessionState{SessionID: "s", Adapter: "claude-code", State: session.StateWorking}
	reader := &uiReader{
		readable: map[string]bool{"s": true},
		screen:   map[string]string{"s": "idle"},
	}
	sink := &recordingSink{}
	o := NewTerminalObserver(newUIRepo(st), reader, uiConsent{map[string]bool{"claude-code": true}},
		func() bool { return true }, sink, bcNopLog{})

	o.tick() // none → none: no edge
	if len(sink.got) != 0 {
		t.Fatalf("no dialog yet: want 0 signals, got %v", sink.got)
	}

	reader.screen["s"] = "Do you want to proceed?"
	o.tick() // none → trust_dialog: rising edge
	o.tick() // trust_dialog → trust_dialog: no edge
	if len(sink.got) != 1 || sink.got[0].ui != backchannel.UIKindTrustDialog {
		t.Fatalf("rising edge: want 1 trust_dialog signal, got %v", sink.got)
	}

	reader.screen["s"] = "back to work"
	o.tick() // trust_dialog → none: clearing edge
	if len(sink.got) != 2 || sink.got[1].ui != backchannel.UIKindNone {
		t.Fatalf("clearing edge: want a UIKindNone signal, got %v", sink.got)
	}
}

func TestObserverGates(t *testing.T) {
	st := &session.SessionState{SessionID: "s", Adapter: "claude-code", State: session.StateWorking}
	dialog := map[string]string{"s": "Do you want to proceed?"}

	t.Run("master toggle off reads nothing", func(t *testing.T) {
		sink := &recordingSink{}
		o := NewTerminalObserver(newUIRepo(st),
			&uiReader{readable: map[string]bool{"s": true}, screen: dialog},
			uiConsent{map[string]bool{"claude-code": true}}, func() bool { return false }, sink, bcNopLog{})
		o.tick()
		if len(sink.got) != 0 {
			t.Fatalf("beta off: want 0 signals, got %v", sink.got)
		}
	})

	t.Run("consent denied reads nothing", func(t *testing.T) {
		sink := &recordingSink{}
		o := NewTerminalObserver(newUIRepo(st),
			&uiReader{readable: map[string]bool{"s": true}, screen: dialog},
			uiConsent{map[string]bool{}}, func() bool { return true }, sink, bcNopLog{})
		o.tick()
		if len(sink.got) != 0 {
			t.Fatalf("consent denied: want 0 signals, got %v", sink.got)
		}
	})

	t.Run("unreadable backend reads nothing", func(t *testing.T) {
		sink := &recordingSink{}
		o := NewTerminalObserver(newUIRepo(st),
			&uiReader{readable: map[string]bool{}, screen: dialog},
			uiConsent{map[string]bool{"claude-code": true}}, func() bool { return true }, sink, bcNopLog{})
		o.tick()
		if len(sink.got) != 0 {
			t.Fatalf("unreadable: want 0 signals, got %v", sink.got)
		}
	})
}

// --- RekeySession (presession→real-session reconciliation, issue #997) ---

func TestRekeySession_CarriesEdgeStateForward(t *testing.T) {
	reader := &uiReader{
		readable: map[string]bool{"old": true, "new": true},
		screen:   map[string]string{"old": "Do you want to proceed?"},
	}
	sink := &recordingSink{}
	consent := uiConsent{map[string]bool{"claude-code": true}}
	o := NewTerminalObserver(newUIRepo(), reader, consent, func() bool { return true }, sink, bcNopLog{})

	// Presession sees the dialog rise.
	o.observe("old", "claude-code")
	if len(sink.got) != 1 || sink.got[0].ui != backchannel.UIKindTrustDialog {
		t.Fatalf("presession rising edge: want 1 trust_dialog signal, got %v", sink.got)
	}

	// Reconciliation retires "old" in favor of "new".
	o.RekeySession("old", "new")

	// A poll of "new" showing the SAME still-open dialog must not re-fire as
	// a fresh rising edge — the carried-forward state already reflects
	// trust_dialog.
	reader.screen["new"] = "Do you want to proceed?"
	o.observe("new", "claude-code")
	if len(sink.got) != 1 {
		t.Fatalf("carried-forward dialog re-fired as a fresh rising edge: got %v", sink.got)
	}

	// When the dialog clears, the falling edge fires against "new" — the
	// live id — not the retired "old".
	reader.screen["new"] = "back to work"
	o.observe("new", "claude-code")
	if len(sink.got) != 2 || sink.got[1].sessionID != "new" || sink.got[1].ui != backchannel.UIKindNone {
		t.Fatalf("clearing edge: want a UIKindNone signal for 'new', got %v", sink.got)
	}
}

func TestRekeySession_NoOpWhenSourceUntracked(t *testing.T) {
	o := NewTerminalObserver(newUIRepo(), &uiReader{}, uiConsent{}, func() bool { return true }, &recordingSink{}, bcNopLog{})

	o.RekeySession("missing", "new") // must not panic or create a spurious entry

	if _, ok := o.lastUI["new"]; ok {
		t.Fatal("RekeySession should not create an entry when the source id was never tracked")
	}
}

// --- fusion (handleTerminalUISignal applies state on the single writer) ---

// newFusionDetector builds a real SessionDetector (so handleTerminalUISignal's
// WithSessionStateLock has a live pidMgr) wired to repo and a capturing
// recorder. git/metrics/pw/broadcaster are nil — the handler never touches them.
func newFusionDetector(repo outbound.SessionRepository) (*SessionDetector, *captureRecorder) {
	rec := &captureRecorder{}
	d := NewSessionDetector(nil, SessionDetectorDeps{
		PW:           nil,
		Repo:         repo,
		Log:          bcNopLog{},
		Git:          nil,
		Metrics:      nil,
		Broadcaster:  nil,
		Version:      "",
		ReadyTTL:     0,
		PIDDiscovers: nil,
		ProcessNames: nil,
		LiveCWDs:     nil,
	})
	d.SetRecorder(rec)
	return d, rec
}

func TestHandleTerminalUISignal_RisingForcesWaiting(t *testing.T) {
	st := &session.SessionState{SessionID: "s", Adapter: "claude-code", State: session.StateWorking}
	d, rec := newFusionDetector(newUIRepo(st))

	d.handleTerminalUISignal(terminalUISignal{sessionID: "s", ui: backchannel.UIKindTrustDialog})

	if st.State != session.StateWaiting {
		t.Fatalf("rising edge: state = %q, want waiting", st.State)
	}
	if st.WaitingStartTime == nil {
		t.Error("rising edge: WaitingStartTime should be set")
	}
	if !rec.has(lifecycle.KindUIDetected) {
		t.Error("rising edge: expected a ui_detected event")
	}
	if !rec.hasTransitionTo(session.StateWaiting) {
		t.Error("rising edge: expected a state_transition to waiting")
	}
}

func TestHandleTerminalUISignal_AlreadyWaitingNoOp(t *testing.T) {
	st := &session.SessionState{SessionID: "s", Adapter: "claude-code", State: session.StateWaiting}
	d, rec := newFusionDetector(newUIRepo(st))

	d.handleTerminalUISignal(terminalUISignal{sessionID: "s", ui: backchannel.UIKindTrustDialog})

	if len(rec.events) != 0 {
		t.Fatalf("already waiting: want no events (no double-count), got %v", rec.events)
	}
}

func TestHandleTerminalUISignal_ClearingReclassifies(t *testing.T) {
	// Dialog cleared while metrics show nothing pending → reclassify out of
	// waiting. Re-classifying from a working base with empty metrics lands on
	// working (the agent resumes); a later transcript event refines it.
	st := &session.SessionState{
		SessionID: "s", Adapter: "claude-code", State: session.StateWaiting,
		Metrics: &session.SessionMetrics{},
	}
	d, rec := newFusionDetector(newUIRepo(st))

	d.handleTerminalUISignal(terminalUISignal{sessionID: "s", ui: backchannel.UIKindNone})

	if st.State == session.StateWaiting {
		t.Fatal("clearing edge: session should have left waiting")
	}
	if !rec.hasTransitionFrom(session.StateWaiting) {
		t.Error("clearing edge: expected a state_transition out of waiting")
	}
}

func TestHandleTerminalUISignal_ClearingWithNilMetricsDoesNotStrand(t *testing.T) {
	// Regression: a session with nil Metrics (e.g. discovered before any
	// transcript) must not be stranded in waiting once the dialog clears.
	// ClassifyState(waiting, nil) is a no-op; re-classifying from a working base
	// avoids the trap and moves the session out of waiting.
	st := &session.SessionState{SessionID: "s", Adapter: "claude-code", State: session.StateWaiting}
	d, _ := newFusionDetector(newUIRepo(st))

	d.handleTerminalUISignal(terminalUISignal{sessionID: "s", ui: backchannel.UIKindNone})

	if st.State == session.StateWaiting {
		t.Fatal("nil-metrics clearing edge: session stranded in waiting")
	}
}

func TestHandleTerminalUISignal_ClearingWhileWorkingNoOp(t *testing.T) {
	// A transcript event already moved the session to working before the
	// clearing edge arrives: leave it alone.
	st := &session.SessionState{SessionID: "s", Adapter: "claude-code", State: session.StateWorking}
	d, rec := newFusionDetector(newUIRepo(st))

	d.handleTerminalUISignal(terminalUISignal{sessionID: "s", ui: backchannel.UIKindNone})

	if st.State != session.StateWorking {
		t.Fatalf("clearing while working: state = %q, want working", st.State)
	}
	if len(rec.events) != 0 {
		t.Fatalf("clearing while working: want no events, got %v", rec.events)
	}
}

// --- ReconcilePreSessionBackchannel (presession→real-session reconciliation,
// issue #997) ---

func TestReconcilePreSessionBackchannel_CarriesWaitingForward(t *testing.T) {
	now := time.Now().Unix()
	old := &session.SessionState{
		SessionID: "proc-42", Adapter: "mistral-vibe", State: session.StateWaiting,
		WaitingStartTime: &now,
	}
	newSt := &session.SessionState{
		SessionID: "session_new", Adapter: "mistral-vibe", State: session.StateReady,
	}
	d, rec := newFusionDetector(newUIRepo(old, newSt))

	d.ReconcilePreSessionBackchannel(old.SessionID, newSt.SessionID)

	if newSt.State != session.StateWaiting {
		t.Fatalf("reconciled session state = %q, want waiting", newSt.State)
	}
	if newSt.WaitingStartTime == nil || *newSt.WaitingStartTime != now {
		t.Fatalf("WaitingStartTime not carried forward: got %v, want %d", newSt.WaitingStartTime, now)
	}
	if !rec.hasTransitionTo(session.StateWaiting) {
		t.Error("expected a state_transition to waiting on the reconciled session")
	}
}

func TestReconcilePreSessionBackchannel_NoOpWhenPresessionNeverWaited(t *testing.T) {
	old := &session.SessionState{SessionID: "proc-42", Adapter: "mistral-vibe", State: session.StateReady}
	newSt := &session.SessionState{SessionID: "session_new", Adapter: "mistral-vibe", State: session.StateReady}
	d, rec := newFusionDetector(newUIRepo(old, newSt))

	d.ReconcilePreSessionBackchannel(old.SessionID, newSt.SessionID)

	if newSt.State != session.StateReady {
		t.Fatalf("untouched session state = %q, want ready", newSt.State)
	}
	if len(rec.events) != 0 {
		t.Fatalf("want no events when the presession never waited, got %v", rec.events)
	}
}

func TestReconcilePreSessionBackchannel_NoOpWhenNewAlreadyWaiting(t *testing.T) {
	oldStart := time.Now().Add(-time.Minute).Unix()
	old := &session.SessionState{
		SessionID: "proc-42", Adapter: "mistral-vibe", State: session.StateWaiting,
		WaitingStartTime: &oldStart,
	}
	newStart := time.Now().Unix()
	newSt := &session.SessionState{
		SessionID: "session_new", Adapter: "mistral-vibe", State: session.StateWaiting,
		WaitingStartTime: &newStart,
	}
	d, rec := newFusionDetector(newUIRepo(old, newSt))

	d.ReconcilePreSessionBackchannel(old.SessionID, newSt.SessionID)

	if newSt.WaitingStartTime == nil || *newSt.WaitingStartTime != newStart {
		t.Fatalf("already-waiting session's own WaitingStartTime should not be overwritten: got %v, want %d", newSt.WaitingStartTime, newStart)
	}
	if len(rec.events) != 0 {
		t.Fatalf("want no events when the reconciled session is already waiting, got %v", rec.events)
	}
}

// captureRecorder records the lifecycle events the detector emits.
type captureRecorder struct{ events []lifecycle.Event }

func (r *captureRecorder) Record(ev lifecycle.Event) { r.events = append(r.events, ev) }
func (r *captureRecorder) Close() error              { return nil }

func (r *captureRecorder) has(k lifecycle.Kind) bool {
	for _, e := range r.events {
		if e.Kind == k {
			return true
		}
	}
	return false
}
func (r *captureRecorder) hasTransitionTo(state string) bool {
	for _, e := range r.events {
		if e.Kind == lifecycle.KindStateTransition && e.NewState == state {
			return true
		}
	}
	return false
}
func (r *captureRecorder) hasTransitionFrom(state string) bool {
	for _, e := range r.events {
		if e.Kind == lifecycle.KindStateTransition && e.PrevState == state {
			return true
		}
	}
	return false
}
