package services

import (
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// #1815 — an errored session must survive a daemon restart. Three things have to
// hold, and this file covers the two that live in this package:
//
//   - the ROW must survive both startup dead-PID deleters (a dead process is the
//     defining evidence for `error`, so the sweeps' own predicate deletes exactly
//     the rows this state exists to preserve), and
//   - the VERDICT must be re-placed at seed time, so the classifier agrees with
//     the state on disk instead of re-deriving `waiting`/`ready` from a frozen
//     transcript.
//
// The tailer's half (LedgerState.SessionError) is pinned in
// core/pkg/tailer/ledger_session_error_test.go; the end-to-end shape, against a
// real restarted daemon, is TestErrorStateSurvivesDaemonRestart.

// TestStartupSweeps_KeepARetainedErrorRow is the defect test for the row half.
// Both deleters are exercised, because exempting one and not the other leaves
// the outcome unchanged — the row simply dies at the second gate instead of the
// first.
func TestStartupSweeps_KeepARetainedErrorRow(t *testing.T) {
	deadPID := deadPIDForRestartTest(t)

	errored := func(updatedAt time.Time) *session.SessionState {
		return &session.SessionState{
			SessionID: "errored-1815",
			State:     session.StateError,
			PID:       deadPID,
			UpdatedAt: updatedAt.Unix(),
			Metrics: &session.SessionMetrics{
				SessionError: &session.SessionError{
					Phase: session.ErrorPhaseTerminal,
					Class: session.ErrorClassProcessDeath,
				},
			},
		}
	}

	t.Run("isStartupZombie keeps it inside the window", func(t *testing.T) {
		pm, _ := restartPIDManager(t)
		if pm.isStartupZombie(errored(time.Now()), nil) {
			t.Fatalf("the startup zombie sweep still deletes an errored session with a dead PID — "+
				"#1815: a crash at 22:00 is gone before the user looks at 08:00 (retention %v)",
				processDeathRetention)
		}
	})

	t.Run("seedAlivePIDs keeps it inside the window", func(t *testing.T) {
		pm, repo := restartPIDManager(t)
		if pm.handleAlivePIDState(errored(time.Now())) {
			t.Errorf("handleAlivePIDState reported the session as alive; it must report not-alive " +
				"(the process really is dead) while still not deleting the row")
		}
		if repo.deletedCount() != 0 {
			t.Fatalf("the seed-time dead-PID path deleted %d errored session(s) — "+
				"#1815: exempting only isStartupZombie leaves this second deleter to take the row anyway",
				repo.deletedCount())
		}
	})

	// The exemption is a RETENTION WINDOW, not a permanent pass. Past the
	// ceiling the ordinary sweeps must reap the row exactly as before, or an
	// errored session becomes immortal — the failure mode the SignalProcessDeath
	// ceiling experiment already caused once.
	t.Run("isStartupZombie reaps it past the window", func(t *testing.T) {
		pm, _ := restartPIDManager(t)
		stale := errored(time.Now().Add(-processDeathRetention - time.Minute))
		if !pm.isStartupZombie(stale, nil) {
			t.Fatalf("an errored session %v old was still exempt — the retention window never closes, "+
				"so a dead session is kept forever", processDeathRetention+time.Minute)
		}
	})

	// A live process is not a zombie whatever its state, so the exemption must
	// not be what decides that. Guards against the exemption being written in a
	// way that only appears to work because it swallows the live case too.
	t.Run("a live errored session is untouched", func(t *testing.T) {
		pm, _ := restartPIDManager(t)
		live := errored(time.Now())
		live.PID = liveProcessForRestartTest(t)
		if pm.isStartupZombie(live, nil) {
			t.Errorf("a live errored session was judged a zombie")
		}
	})
}

// TestRetainedErrorAcrossRestart_OnlyErroredRows pins the predicate's scope. It
// is consulted before EVERY dead-PID deletion, so a version that answered true
// for an ordinary state would disable the startup sweep wholesale — the #242
// regression, reintroduced through the back door.
func TestRetainedErrorAcrossRestart_OnlyErroredRows(t *testing.T) {
	for _, st := range session.CanonicalStates() {
		state := &session.SessionState{State: st, UpdatedAt: time.Now().Unix()}
		want := st == session.StateError
		if got := retainedErrorAcrossRestart(state, time.Now(), processDeathRetention); got != want {
			t.Errorf("retainedErrorAcrossRestart(state=%q) = %v, want %v", st, got, want)
		}
	}
	if retainedErrorAcrossRestart(nil, time.Now(), processDeathRetention) {
		t.Errorf("retainedErrorAcrossRestart(nil) = true, want false")
	}
}

// TestSeedRestoreErrorVerdict_ProcessDeathOutranksAFrozenTranscript is the defect
// test for the verdict half, and specifically for the part a ledger cannot fix.
//
// A process-death verdict is synthesized by the daemon from the OS view of the
// process; nothing about it is ever written to a transcript, so no amount of
// tailer persistence reconstructs it. Meanwhile the transcript a crashed agent
// leaves behind ends mid-turn, which the transcript-tier rules read as work in
// flight. Restoring only Metrics.SessionError therefore is NOT enough: the
// session_error rule sits below those rules and never gets a turn. Only the
// re-placed hold restores Metrics.ProcessDeath, and with it the process_death
// rule's higher rung.
func TestSeedRestoreErrorVerdict_ProcessDeathOutranksAFrozenTranscript(t *testing.T) {
	d := newSeedRestoreDetector(t)

	persistedAt := time.Now().Add(-2 * time.Hour)
	state := &session.SessionState{
		SessionID: "crashed-1815",
		State:     session.StateError,
		UpdatedAt: persistedAt.Unix(),
		Metrics: &session.SessionMetrics{
			SessionError: &session.SessionError{
				Phase:   session.ErrorPhaseTerminal,
				Class:   session.ErrorClassProcessDeath,
				Message: "agent process (pid 4242) exited mid-turn — process watcher",
			},
		},
	}
	persisted := persistedErrorVerdict(state)

	// What RefreshMetrics does to the row a line later: a merge against a fresh
	// tailer pass carrying nil. Reproduced literally rather than mocked, because
	// this erasure IS the bug the restore exists to undo.
	//
	// HasOpenToolCall is the frozen transcript a crashed agent leaves behind — an
	// assistant tool_use with no result — and it is what makes this test hard:
	// the transcript-tier rules read it as work in flight and would paint the
	// session `waiting`, well above where the session_error rule sits.
	state.Metrics = &session.SessionMetrics{LastEventType: "assistant", HasOpenToolCall: true}

	d.seedRestoreErrorVerdict(state, persisted)

	if !state.Metrics.ProcessDeath {
		t.Fatalf("Metrics.ProcessDeath is false after the seed restore — #1815: without it the " +
			"process_death rule cannot fire and a frozen mid-turn transcript re-paints the session")
	}
	if state.Metrics.SessionError == nil {
		t.Fatalf("Metrics.SessionError is nil after the seed restore — the verdict's payload was lost")
	}
	if got := state.Metrics.SessionError.Class; got != session.ErrorClassProcessDeath {
		t.Errorf("restored error class = %q, want %q", got, session.ErrorClassProcessDeath)
	}

	newState, _ := ClassifyState(session.StateError, state.Metrics)
	if newState != session.StateError {
		t.Errorf("the classifier reads %q after the restore, want %q — the seed's own "+
			"re-classification still disagrees with the state on disk", newState, session.StateError)
	}

	// Retention is registered by the SECOND half of the seed, after the
	// classifier has ruled — see seedRetainRestoredError for why the ordering is
	// load-bearing. The window must CONTINUE from when the previous daemon
	// ruled, not restart: anchoring at the restart would hand a machine that
	// reboots daily an unreachable ceiling.
	state.State = session.StateError // what ClassifyState just concluded, above
	d.seedRetainRestoredError(state, persisted)

	at, ok := d.processDeathVerdictAt("crashed-1815")
	if !ok {
		t.Fatalf("no retention entry was registered — retainAsProcessDeath falls through to " +
			"diedMidTurn, which is false for a row already in error, and the periodic liveness " +
			"sweep reaps the row this restore just rescued a few seconds later")
	}
	if !at.Equal(persistedAt.Truncate(time.Second)) {
		t.Errorf("retention re-registered at %s, want the persisted %s — the window restarted "+
			"instead of continuing", at, persistedAt.Truncate(time.Second))
	}
}

// TestSeedRestoreErrorVerdict_IgnoresANonErroredRow is a guard on the input, not
// on the outcome: a SessionError left on a row that has since recovered must not
// resurrect the error at the next boot.
func TestSeedRestoreErrorVerdict_IgnoresANonErroredRow(t *testing.T) {
	recovered := &session.SessionState{
		SessionID: "recovered-1815",
		State:     session.StateReady,
		UpdatedAt: time.Now().Unix(),
		Metrics: &session.SessionMetrics{
			SessionError: &session.SessionError{Phase: session.ErrorPhaseTerminal, Class: "provider"},
		},
	}
	if got := persistedErrorVerdict(recovered); got != nil {
		t.Errorf("persistedErrorVerdict returned %+v for a %s row — a stale payload on a recovered "+
			"session must not be restored", got, session.StateReady)
	}
}

// --- helpers -----------------------------------------------------------------
//
// Local to this file and to the INTERNAL package, deliberately: the predicates
// under test (isStartupZombie, handleAlivePIDState, retainedErrorAcrossRestart,
// seedRestoreErrorVerdict) are all unexported, so the shared builders in
// package services_test cannot reach them.

// restartLog swallows log output. The paths under test log heavily on the way
// through, and none of it is what is being asserted.
type restartLog struct{ outbound.Logger }

func (restartLog) LogInfo(_, _, _ string)  {}
func (restartLog) LogError(_, _, _ string) {}

// restartRepo is a minimal in-memory SessionRepository that COUNTS deletions —
// which is the observable the sweep tests actually assert on. A sweep that
// deletes the row is the defect; a sweep that merely declines to report it alive
// is not.
type restartRepo struct {
	mu      sync.Mutex
	states  []*session.SessionState
	deleted []string
}

func (r *restartRepo) Load(id string) (*session.SessionState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.states {
		if s.SessionID == id {
			return s, nil
		}
	}
	return nil, nil
}

func (r *restartRepo) Save(state *session.SessionState) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, s := range r.states {
		if s.SessionID == state.SessionID {
			r.states[i] = state
			return nil
		}
	}
	r.states = append(r.states, state)
	return nil
}

func (r *restartRepo) Delete(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deleted = append(r.deleted, id)
	return nil
}

func (r *restartRepo) ListAll() ([]*session.SessionState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*session.SessionState(nil), r.states...), nil
}

func (r *restartRepo) deletedCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.deleted)
}

// restartPIDManager builds a PIDManager over a fresh counting repo and returns
// both, so a test can assert on what the sweep removed.
func restartPIDManager(t *testing.T) (*PIDManager, *restartRepo) {
	t.Helper()
	repo := &restartRepo{}
	pm := NewPIDManager(PIDManagerDeps{
		Repo:     repo,
		Log:      restartLog{},
		ReadyTTL: 10 * time.Minute,
	})
	return pm, repo
}

// newSeedRestoreDetector builds the minimum SessionDetector the seed-time
// restore touches: a signal-hold store, the verdict registry, a clock and a log.
func newSeedRestoreDetector(t *testing.T) *SessionDetector {
	t.Helper()
	return NewSessionDetector(nil, SessionDetectorDeps{Log: restartLog{}})
}

// deadPIDForRestartTest spawns and reaps a process, returning a PID known to be
// dead. Skips rather than guesses when the kernel recycles it first — a recycled
// PID is alive, which would invert every assertion built on it.
func deadPIDForRestartTest(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start true: %v", err)
	}
	pid := cmd.Process.Pid
	_ = cmd.Wait()
	if err := syscall.Kill(pid, 0); err == nil {
		t.Skipf("dead PID %d was recycled before the test could observe it", pid)
	}
	return pid
}

// liveProcessForRestartTest returns the PID of a child that outlives the test.
func liveProcessForRestartTest(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd.Process.Pid
}

// TestSeedRestoreErrorVerdict_DropsAHoldWhoseTurnActuallyFinished is a LOCK: it
// passes before #1815 by construction (the restore did not exist, so nothing was
// ever applied) and pins the direction the restore must NOT break.
//
// The exit edge places a process-death hold PROVISIONALLY, because at the moment
// a process dies the daemon cannot tell a crash from a clean exit — a headless
// agent writes its turn-ending line and exits microseconds later. Overlay's
// staleness rule is the actual verdict. Restoring the hold at seed time must go
// through that same rule rather than around it, or every clean `claude -p` run
// on disk at boot would come back red.
func TestSeedRestoreErrorVerdict_DropsAHoldWhoseTurnActuallyFinished(t *testing.T) {
	d := newSeedRestoreDetector(t)

	state := &session.SessionState{
		SessionID: "finished-1815",
		State:     session.StateError,
		UpdatedAt: time.Now().Unix(),
		Metrics: &session.SessionMetrics{
			SessionError: &session.SessionError{
				Phase: session.ErrorPhaseTerminal,
				Class: session.ErrorClassProcessDeath,
			},
		},
	}
	persisted := persistedErrorVerdict(state)

	// The turn HAD in fact ended before the process exited.
	state.Metrics = &session.SessionMetrics{LastEventType: "turn_done"}

	d.seedRestoreErrorVerdict(state, persisted)

	if state.Metrics.ProcessDeath {
		t.Errorf("a session whose turn had finished was pinned as a process death — " +
			"the seed restore bypassed Overlay's staleness rule, so every clean headless " +
			"exit sitting on disk at boot comes back red")
	}
}

// TestStartupSweeps_KeepARetainedErrorOrphanRow covers the THIRD deleter, found
// in review after the first two were already exempted: seedAlivePIDs' PID==0
// branch, which has nothing to do with a dead PID at all.
//
// Reachable for a headless run (`claude -p`, `codex exec`) that fails on a
// provider error and exits before PID discovery ever binds — the row is `error`
// with PID==0, and an errored session's transcript is stale by definition once
// orphanTranscriptAge has passed. Exempting only the two dead-PID paths left
// this one to take exactly the rows they had just spared.
func TestStartupSweeps_KeepARetainedErrorOrphanRow(t *testing.T) {
	transcript := filepath.Join(t.TempDir(), "orphan.jsonl")
	if err := os.WriteFile(transcript, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	// Backdated past orphanTranscriptAge, which is what makes isOrphanAtSeed
	// say yes. An errored session reaches this state simply by sitting there.
	old := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(transcript, old, old); err != nil {
		t.Fatalf("backdate transcript: %v", err)
	}

	state := &session.SessionState{
		SessionID:      "orphan-error-1815",
		State:          session.StateError,
		PID:            0,
		TranscriptPath: transcript,
		CWD:            filepath.Dir(transcript),
		UpdatedAt:      time.Now().Unix(),
		Metrics: &session.SessionMetrics{
			SessionError: &session.SessionError{Phase: session.ErrorPhaseTerminal, Class: "provider"},
		},
	}

	// Precondition — without it this test would pass on a row the branch never
	// reaches, and prove nothing. isOrphanAtSeed must genuinely want this row.
	if !isOrphanAtSeed(state) {
		t.Fatalf("isOrphanAtSeed is false for the fixture, so the branch under test is unreachable " +
			"and this test cannot discriminate")
	}

	pm, repo := restartPIDManager(t)
	pm.seedAlivePIDs([]*session.SessionState{state})
	if repo.deletedCount() != 0 {
		t.Fatalf("the PID==0 orphan branch deleted %d errored session(s) — #1815: a headless run "+
			"that failed before PID discovery bound is deleted at the next daemon start",
			repo.deletedCount())
	}
}

// TestRetainAsProcessDeath_KeepsARestoredRowThroughThePeriodicSweep is the
// regression test for the defect that made the startup exemptions worthless for
// every class except process_death.
//
// The startup sweeps are not the only deleter: SweepDeadPIDs ticks every few
// seconds into retainAsProcessDeath, and a row restored from a previous daemon
// run has no verdict of its own in this process. It therefore fell through to
// diedMidTurn — which is false for anything already in `error` — and was deleted
// about five seconds AFTER the restart rather than at it. The row was rescued at
// boot and gone before a human could look, which is the issue's own outcome with
// a delay in front of it.
func TestRetainAsProcessDeath_KeepsARestoredRowThroughThePeriodicSweep(t *testing.T) {
	// A TRANSCRIPT-derived class on purpose: the process_death class is retained
	// by its own re-registered verdict, so testing with it would pass without
	// the branch this test is about.
	state := &session.SessionState{
		SessionID: "restored-1815",
		State:     session.StateError,
		UpdatedAt: time.Now().Add(-2 * time.Hour).Unix(),
		Metrics: &session.SessionMetrics{
			SessionError: &session.SessionError{Phase: session.ErrorPhaseTerminal, Class: "rate_limit_error"},
		},
	}

	d := newSeedRestoreDetector(t)

	// Through the seed path, as a real restart does: the row comes off disk in
	// `error`, the classifier agrees, and seedRetainRestoredError registers the
	// retention entry the sweep then reads.
	d.seedRetainRestoredError(state, persistedErrorVerdict(state))

	if !d.retainAsProcessDeath(state, 4242, "pid exited (ESRCH)") {
		t.Fatalf("the periodic liveness sweep reaps an errored row restored from a previous daemon " +
			"run — #1815: the startup exemption is undone a few seconds later, so the row is still " +
			"gone before anyone looks")
	}

	// And the window still closes: past the retention the sweep must reap it
	// exactly as it always did, or an errored row becomes immortal.
	d2 := newSeedRestoreDetector(t)
	stale := *state
	stale.SessionID = "restored-stale-1815"
	stale.UpdatedAt = time.Now().Add(-processDeathRetention - time.Hour).Unix()
	d2.seedRetainRestoredError(&stale, persistedErrorVerdict(&stale))
	if d2.retainAsProcessDeath(&stale, 4242, "pid exited (ESRCH)") {
		t.Errorf("an errored row past the retention window was still retained — the window never " +
			"closes and the row is kept forever")
	}
}

// TestSeedRestoreErrorVerdict_TranscriptClassPlacesNoHold pins the narrowing the
// review forced, and it is a real behavioural assertion rather than a scope note.
//
// Re-placing a SignalSessionError hold looks like the symmetric thing to do and
// is wrong: that row's apply writes its payload into any empty SessionError slot,
// so the moment the tailer's clearing rule fires the hold writes the error back
// in and the session reads `error` through the whole of the user's recovery turn.
// For the adapters with no Stop hook (aider, opencode, pi) the row's only
// staleness rule never fires at all, so that becomes the full 12h ceiling. The
// ledger already carries this class across a restart, and the tailer owns
// clearing it.
func TestSeedRestoreErrorVerdict_TranscriptClassPlacesNoHold(t *testing.T) {
	d := newSeedRestoreDetector(t)

	state := &session.SessionState{
		SessionID: "provider-error-1815",
		State:     session.StateError,
		UpdatedAt: time.Now().Unix(),
		Metrics: &session.SessionMetrics{
			SessionError: &session.SessionError{Phase: session.ErrorPhaseTerminal, Class: "rate_limit_error"},
		},
	}
	persisted := persistedErrorVerdict(state)

	// The user came back and the turn succeeded: the tailer cleared its sticky
	// error, so the fresh pass carries nil. No Stop hook — the case where a
	// re-placed hold would have no end-of-life at all.
	state.Metrics = &session.SessionMetrics{LastEventType: "assistant", HookTurnDone: false}

	d.seedRestoreErrorVerdict(state, persisted)
	d.signals.Overlay(state.SessionID, state.Metrics, time.Now())

	if state.Metrics.SessionError != nil {
		t.Errorf("a restored hold wrote the cleared error back onto a recovered session (%+v) — "+
			"the session reads `error` through the user's whole recovery turn, and for a "+
			"hookless adapter for the full retention window", state.Metrics.SessionError)
	}
}

// TestSeedRestoreErrorVerdict_NoVerdictWhenOverlayDropsTheHold pins the ordering
// fix: the verdict registry is written only AFTER Overlay has ruled, and only
// when the hold actually applied.
//
// The exit edge places a process-death hold provisionally — at the moment a
// process dies the daemon cannot tell a crash from a clean exit — and Overlay's
// IsAgentDone rule is the verdict. Registering before that ruling shields a row
// Overlay just judged NOT a crash from the reaper for the whole retention
// window, which is worse than the pre-fix behaviour: without the restore such a
// row is reaped on the next sweep.
func TestSeedRestoreErrorVerdict_NoVerdictWhenOverlayDropsTheHold(t *testing.T) {
	d := newSeedRestoreDetector(t)

	state := &session.SessionState{
		SessionID: "clean-exit-1815",
		State:     session.StateError,
		UpdatedAt: time.Now().Unix(),
		Metrics: &session.SessionMetrics{
			SessionError: &session.SessionError{
				Phase: session.ErrorPhaseTerminal,
				Class: session.ErrorClassProcessDeath,
			},
		},
	}
	persisted := persistedErrorVerdict(state)
	state.Metrics = &session.SessionMetrics{LastEventType: "turn_done"} // the turn HAD finished

	d.seedRestoreErrorVerdict(state, persisted)

	if state.Metrics.ProcessDeath {
		t.Fatalf("Overlay should have dropped the hold as stale for a finished turn")
	}

	// The hold never applied, so the classifier settles the row away from
	// `error` — and the retention registration is gated on that final state.
	newState, _ := ClassifyState(session.StateError, state.Metrics)
	if newState == session.StateError {
		t.Fatalf("the classifier still reads %q for a finished turn, so the gate below "+
			"is unreachable and this test cannot discriminate", newState)
	}
	state.State = newState
	d.seedRetainRestoredError(state, persisted)

	if _, ok := d.processDeathVerdictAt("clean-exit-1815"); ok {
		t.Errorf("retention was registered for a row Overlay judged NOT a crash — it is now " +
			"shielded from the reaper for the whole retention window, where without the restore " +
			"it would be reaped on the next sweep")
	}
}

// TestSeedRetainRestoredError_SkipsARecoveredSession is the gate the ordering
// buys, asserted directly rather than left implicit.
//
// If the transcript grew while the daemon was down and the user's next turn
// succeeded, the tailer's clearing rule fires during RefreshMetrics, the
// classifier moves the row off `error`, and NOTHING should be registered — a
// recovered session shielded from the reaper for twelve hours would be this
// fix's own mirror-image bug.
func TestSeedRetainRestoredError_SkipsARecoveredSession(t *testing.T) {
	d := newSeedRestoreDetector(t)

	state := &session.SessionState{
		SessionID: "recovered-across-restart-1815",
		State:     session.StateError,
		UpdatedAt: time.Now().Unix(),
		Metrics: &session.SessionMetrics{
			SessionError: &session.SessionError{Phase: session.ErrorPhaseTerminal, Class: "provider"},
		},
	}
	persisted := persistedErrorVerdict(state)

	// The classifier's verdict on the refreshed metrics: recovered.
	state.State = session.StateReady
	d.seedRetainRestoredError(state, persisted)

	if _, ok := d.processDeathVerdictAt("recovered-across-restart-1815"); ok {
		t.Errorf("a recovered session was registered for retention — it is now shielded from " +
			"the reaper for the whole window despite no longer being in error")
	}
}
