package services

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"irrlicht/core/domain/session"
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
	// classifier has ruled — see seedRetainErroredRows for why the ordering is
	// load-bearing. The window must CONTINUE from when the previous daemon
	// ruled, not restart: anchoring at the restart would hand a machine that
	// reboots daily an unreachable ceiling.
	state.State = session.StateError // what ClassifyState just concluded, above
	d.seedRetainErroredRows([]*session.SessionState{state})

	at, ok := d.processDeathVerdictAt("crashed-1815")
	if !ok {
		t.Fatalf("no retention entry was registered — retainAsProcessDeath falls through to " +
			"DiedMidTurn, which is false for a row already in error, and the periodic liveness " +
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
		Log:      bornLog{},
		ReadyTTL: 10 * time.Minute,
	})
	return pm, repo
}

// newSeedRestoreDetector builds the minimum SessionDetector the seed-time
// restore touches: a signal-hold store, the verdict registry, a clock and a log.
func newSeedRestoreDetector(t *testing.T) *SessionDetector {
	t.Helper()
	return NewSessionDetector(nil, SessionDetectorDeps{Log: bornLog{}})
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
// DiedMidTurn — which is false for anything already in `error` — and was deleted
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
	// `error`, the classifier agrees, and seedRetainErroredRows registers the
	// retention entry the sweep then reads.
	d.seedRetainErroredRows([]*session.SessionState{state})

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
	d2.seedRetainErroredRows([]*session.SessionState{&stale})
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

	// A LOCK, and the precondition for the assertion below: the exit edge places
	// a process-death hold PROVISIONALLY, because at the moment a process dies
	// the daemon cannot tell a crash from a clean exit — a headless agent writes
	// its turn-ending line and exits microseconds later. Overlay's staleness rule
	// is the actual verdict, and the seed restore must go THROUGH it rather than
	// around it, or every clean `claude -p` run sitting on disk at boot comes
	// back red.
	if state.Metrics.ProcessDeath {
		t.Fatalf("a session whose turn had finished was pinned as a process death — the seed " +
			"restore bypassed Overlay's staleness rule")
	}

	// The hold never applied, so the classifier settles the row away from
	// `error` — and the retention registration is gated on that final state.
	newState, _ := ClassifyState(session.StateError, state.Metrics)
	if newState == session.StateError {
		t.Fatalf("the classifier still reads %q for a finished turn, so the gate below "+
			"is unreachable and this test cannot discriminate", newState)
	}
	state.State = newState
	d.seedRetainErroredRows([]*session.SessionState{state})

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
func TestSeedRetainErroredRows_SkipsARecoveredSession(t *testing.T) {
	d := newSeedRestoreDetector(t)

	state := &session.SessionState{
		SessionID: "recovered-across-restart-1815",
		State:     session.StateError,
		UpdatedAt: time.Now().Unix(),
		Metrics: &session.SessionMetrics{
			SessionError: &session.SessionError{Phase: session.ErrorPhaseTerminal, Class: "provider"},
		},
	}
	// The classifier's verdict on the refreshed metrics: recovered.
	state.State = session.StateReady
	d.seedRetainErroredRows([]*session.SessionState{state})

	if _, ok := d.processDeathVerdictAt("recovered-across-restart-1815"); ok {
		t.Errorf("a recovered session was registered for retention — it is now shielded from " +
			"the reaper for the whole window despite no longer being in error")
	}
}

// TestEveryReaperConsultsTheErrorExemption is a STRUCTURAL tripwire, not a
// behavioural test: it fails when a function that deletes session rows does not
// consult the #1815 error exemption.
//
// WHY A STATIC CHECK RATHER THAN MORE CASES. The first cut of this fix inlined
// the exemption at the three deleters its author had found, and a fourth —
// sweepStaleSnapshot's ready-TTL branch — went on deleting a PID==0 errored row
// after 30 minutes, inside the twelve-hour window. Nothing structurally
// connected "a place that deletes rows" to "consult the exemption", so the miss
// was invisible: every behavioural test passed, because none exercised that
// particular reaper. A per-site inventory in a doc comment has the same
// weakness — the one this fix removed was written already stale.
//
// BOTH SETS ARE DERIVED FROM THE SOURCE. NEITHER IS TYPED BY HAND. That is the
// whole design, and each half was learned from this check failing to fail:
//
//   - DELETION was originally recognised by a hand-typed list of three wrapper
//     names (deleteWithChildren / deleteSession / removeSessionUntracked). A
//     reaper written as `pm.repo.Delete(id)` was invisible to it — and that
//     idiom is ALREADY in this file, in cleanupStalePIDHolders. A review probe
//     added a sixth reaper using it and this test stayed GREEN. Deletion is now
//     seeded from the PRIMITIVE (a `.repo.Delete(...)` call site) and grown
//     transitively, so a wrapper is discovered rather than declared.
//   - DELEGATION was originally a hand-typed list of predicate names. Deleting
//     the guard from a listed predicate left the test GREEN, because the caller
//     was still calling a name on the list. It is now grown the same way.
//
// A check that cannot fail reads exactly like one that found nothing, which is
// what AGENTS.md forbids; the vacuity guards at the bottom are the other half of
// that, and tools/lib/error-retention-mutations_test.sh runs the mutations that
// prove both directions on every `preflight.sh --only tools`.
//
// The exempt map is the deliberate part: a deleter on it must NOT consult the
// exemption, with the reason stated.
func TestEveryReaperConsultsTheErrorExemption(t *testing.T) {
	exempt := map[string]string{
		"deleteSession":                      "the shared choke point; guarding here would strand an errored CHILD whose parent was deleted, leaving a dangling ParentSessionID nothing can collect",
		"deleteWithChildren":                 "same choke-point argument, and it would re-shield a row whose verdict HandlePIDAssigned deliberately retired (TestHandlePIDAssigned_RetiresAProcessDeathVerdict)",
		"cleanupChildren":                    "children are reaped on their parent's lifecycle, not their own state",
		"reapStaleChild":                     "same: a child's retention belongs to its parent",
		"reapUnboundReadyGhost":              "ready-state ghosts only; an errored row never reaches it",
		"dedupeByPID":                        "supersession, not reaping — the surviving row carries the identity",
		"cleanupStalePIDHolders":             "supersession, not reaping: its inputs are the OTHER rows holding a PID that a new session just bound, so the identity has moved to the survivor. Retaining a superseded errored row would leave a duplicate of a session the user can still see, under an id nothing will ever write to again — and it fires onSessionSuperseded, which carries state FORWARD onto the new row rather than dropping it",
		"sweepSupersededPreSessions":         "presession supersession, not reaping — only `proc-` rows, which never carry a verdict",
		"sweepSupersededPreSessionsPeriodic": "same, on the periodic timer — verified to `continue` on any id without the `proc-` prefix",
		"removeSessionUntracked":             "the untracked-removal primitive the two supersession sweeps call",
		"HandleProcessExit":                  "routes through onProcessDiedMidTurn (retainAsProcessDeath), which owns the live-path retention via the verdict map",
		"reapDeadOrInfraPID":                 "the periodic sweep's reaper: it deletes ONLY by calling HandleProcessExit, so its retention is the verdict map's, not retainsError's (verified — no other deleting call in its body)",
		"HandlePIDAssigned":                  "deletes ONLY via cleanupStalePIDHolders (verified), i.e. supersession; and it is the function that deliberately RETIRES a verdict when a process comes back, so consulting the exemption here would undo its own job",
		"TryDiscoverPID":                     "reaches deletion only through HandlePIDAssigned (verified — its sole deleting call), inheriting that entry's supersession reason",
		"DiscoverPIDWithRetry":               "calls only TryDiscoverPID (verified), so it inherits the same one",
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "pid_manager.go", nil, 0)
	if err != nil {
		t.Fatalf("parse pid_manager.go: %v", err)
	}

	// calls[fn] = the method names fn invokes; primitive[fn] = fn contains a
	// `<recv>.repo.Delete(...)` call site, which is what actually removes a row.
	calls := map[string]map[string]bool{}
	primitive := map[string]bool{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		seen := map[string]bool{}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			seen[sel.Sel.Name] = true
			// `pm.repo.Delete(...)` — the selector's own receiver is `.repo`.
			if sel.Sel.Name == "Delete" {
				if inner, ok := sel.X.(*ast.SelectorExpr); ok && inner.Sel.Name == "repo" {
					primitive[fn.Name.Name] = true
				}
			}
			return true
		})
		calls[fn.Name.Name] = seen
	}

	// Grow both relations to a fixed point over the call graph.
	deletes := grow(calls, primitive)
	consults := grow(calls, map[string]bool{"retainsError": true})

	var checked int
	for fn := range calls {
		if !deletes[fn] || exempt[fn] != "" {
			continue
		}
		checked++
		if !consults[fn] {
			t.Errorf("%s deletes session rows but nothing on its call path consults retainsError "+
				"— #1815: an errored row within its retention window is deleted by this path. "+
				"Either consult the exemption (directly or via a predicate that does), or add "+
				"%s to this test's `exempt` map with the reason.", fn, fn)
		}
	}

	// VACUITY GUARDS. Absence of a finding and inability to look must not
	// produce the same output. Each of these fails if a rename or a refactor
	// leaves the walk matching nothing — the state in which this test would
	// otherwise pass forever.
	if len(primitive) == 0 {
		t.Fatalf("no `.repo.Delete(...)` call site was found in pid_manager.go — the deletion " +
			"primitive was renamed or reshaped, and this test is no longer checking anything")
	}
	if len(consults) < 2 {
		t.Fatalf("nothing was found to consult retainsError — the delegation walk is inert")
	}
	// Derived, not typed: the count is whatever the walk found, so this cannot
	// go stale the way a hand-written "three deleters exist today" did.
	if checked == 0 {
		t.Fatalf("found %d deleting function(s), all of them exempt — no reaper is actually "+
			"being checked", len(deletes))
	}
	t.Logf("checked %d non-exempt deleter(s) of %d total; %d function(s) consult the exemption",
		checked, len(deletes), len(consults))
}

// grow closes `seed` over the call graph in `calls`: a function joins the set
// when it calls anything already in it. Shared by the deletion and delegation
// relations, which differ only in their seed.
func grow(calls map[string]map[string]bool, seed map[string]bool) map[string]bool {
	out := map[string]bool{}
	for k := range seed {
		out[k] = true
	}
	for changed := true; changed; {
		changed = false
		for fn, seen := range calls {
			if out[fn] {
				continue
			}
			for name := range seen {
				if out[name] {
					out[fn] = true
					changed = true
					break
				}
			}
		}
	}
	return out
}

// TestStaleSweep_KeepsARetainedErrorRow is the behavioural half of the fifth
// deleter the structural tripwire above exists to prevent.
//
// sweepStaleSnapshot's `snap.pid == 0` branch deletes after readyTTL — 30
// minutes by default — which is inside the twelve-hour window an errored row is
// promised. It takes exactly the shape #1815's own comments describe: a headless
// run that fails on a provider error and exits before PID discovery binds.
func TestStaleSweep_KeepsARetainedErrorRow(t *testing.T) {
	pm, repo := restartPIDManager(t)

	state := &session.SessionState{
		SessionID: "stale-error-1815",
		State:     session.StateError,
		PID:       0,
		// Well past readyTTL, well inside the error retention window.
		UpdatedAt: time.Now().Add(-2 * time.Hour).Unix(),
		Metrics: &session.SessionMetrics{
			SessionError: &session.SessionError{Phase: session.ErrorPhaseTerminal, Class: "provider"},
		},
	}
	pm.sweepStaleSnapshot(snapshotForTest(state))

	if repo.deletedCount() != 0 {
		t.Fatalf("the ready-TTL liveness sweep deleted an errored row %v old — #1815: the "+
			"twelve-hour promise is really readyTTL (%v) for a PID==0 errored session",
			2*time.Hour, pm.readyTTL)
	}

	// The window still closes: past the retention, the sweep reaps as before.
	pm2, repo2 := restartPIDManager(t)
	stale := *state
	stale.UpdatedAt = time.Now().Add(-processDeathRetention - time.Hour).Unix()
	pm2.sweepStaleSnapshot(snapshotForTest(&stale))
	if repo2.deletedCount() != 1 {
		t.Errorf("an errored row past the retention window was not reaped by the stale sweep — "+
			"the exemption never expires (deleted %d)", repo2.deletedCount())
	}
}

// snapshotForTest builds the livenessSnapshot sweepStaleSnapshot consumes.
func snapshotForTest(state *session.SessionState) livenessSnapshot {
	return livenessSnapshot{
		state:        state,
		pid:          state.PID,
		sessionState: state.State,
		updatedAt:    state.UpdatedAt,
	}
}

// TestSeedRetention_CoversARowTheSeedPassSkips is the regression test for the
// gap an independent review found in the first cut: the retention registration
// lived inside seedReevaluateOne, which is reached only for rows that clear
// seedReevaluateStates' CONSENT GATE and have a TranscriptPath.
//
// A row the seed pass skips therefore got no entry, and the periodic liveness
// sweep deleted it about five seconds after the restart — retainAsProcessDeath
// finds no verdict, and DiedMidTurn is false for anything already in `error`.
// The startup exemption spared the row and then nothing kept it.
//
// NOT A HYPOTHETICAL COMBINATION. Both skip conditions are ordinary: an adapter
// whose observe permission is pending (which is EVERY adapter on a fresh
// IRRLICHT_HOME until the wizard is answered), and a row with no TranscriptPath
// (a store-backed adapter's session, or one whose transcript was removed). The
// user most likely to hit it is the one who has not finished onboarding.
func TestSeedRetention_CoversARowTheSeedPassSkips(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*session.SessionState)
		gate   func(*SessionDetector)
	}{
		{
			name:   "no transcript path",
			mutate: func(s *session.SessionState) { s.TranscriptPath = "" },
			gate:   func(*SessionDetector) {},
		},
		{
			name:   "observe consent not granted",
			mutate: func(s *session.SessionState) { s.TranscriptPath = "/nonexistent/x.jsonl" },
			gate:   func(d *SessionDetector) { d.SetConsentGate(func(string) bool { return false }) },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &restartRepo{}
			state := &session.SessionState{
				SessionID: "skipped-" + tc.name,
				Adapter:   "claude-code",
				State:     session.StateError,
				PID:       4242,
				UpdatedAt: time.Now().Add(-time.Hour).Unix(),
				Metrics: &session.SessionMetrics{
					SessionError: &session.SessionError{
						Phase: session.ErrorPhaseTerminal,
						Class: session.ErrorClassProcessDeath,
					},
				},
			}
			tc.mutate(state)
			if err := repo.Save(state); err != nil {
				t.Fatalf("seed: %v", err)
			}

			d := NewSessionDetector(nil, SessionDetectorDeps{Log: bornLog{}, Repo: repo})
			tc.gate(d)
			d.seedFromDisk()

			// The SUBJECT: what the periodic liveness sweep decides a few
			// seconds later. Retention has to be independent of whether the
			// seed pass examined this row at all.
			if !d.retainAsProcessDeath(state, state.PID, "test: pid exited") {
				t.Fatalf("the periodic sweep reaps an errored row the seed pass skipped (%s) — "+
					"#1815: the retention registration is gated behind the consent/transcript "+
					"check, so the row is spared at startup and deleted ~5s later", tc.name)
			}
		})
	}
}

// TestForgetSessionScopedState_DropsTheProcessDeathVerdict settles the review's
// finding 3 with a run rather than with reasoning.
//
// THE SCENARIO, and it is reachable: a session dies mid-turn and gets a verdict
// entry; twelve hours later the window closes and the row is reaped; the user
// then runs `claude --resume <uuid>` under the SAME session id, and that session
// dies mid-turn too. retainAsProcessDeath finds the stale entry, takes the
// EXPIRY branch (`now - at >= retention`), and returns false — so the second
// death is DELETED rather than converted to `error`, and the user loses exactly
// the signal #1800 exists to give them. It is silent and it never self-heals:
// forgetProcessDeathVerdict runs only on that expiry branch, which returns false
// every time, so the entry is dropped only after it has already eaten one death.
//
// #1800 shipped this map without adding it to forgetSessionScopedState, whose
// own doc says the rule for the next per-session map is "add it HERE, not to a
// call site" — and it was missed anyway. This diff makes it materially more
// likely by registering an entry at EVERY seed for EVERY errored row, where
// before only a live mid-turn death created one.
func TestForgetSessionScopedState_DropsTheProcessDeathVerdict(t *testing.T) {
	d := newSeedRestoreDetector(t)
	const sid = "recycled-uuid-1815"

	// A verdict from a session that has since aged out and been reaped.
	d.restoreErrorRetention(sid, time.Now().Add(-processDeathRetention-time.Hour))
	if _, ok := d.processDeathVerdictAt(sid); !ok {
		t.Fatalf("precondition failed: no verdict registered, so this test cannot discriminate")
	}

	// The row is deleted. This is the seam every per-session map is supposed to
	// be dropped through.
	d.forgetSessionScopedState(sid)

	if _, ok := d.processDeathVerdictAt(sid); ok {
		t.Fatalf("the process-death verdict survived the row's deletion — a session id reused by " +
			"`claude --resume` inherits it, and its FIRST mid-turn death takes the expiry branch " +
			"and is deleted instead of going red (#1800's map was never added to " +
			"forgetSessionScopedState; its own doc says to add it here)")
	}

	// And the consequence the entry would have caused: a fresh mid-turn death
	// under the recycled id must convert to `error`, not be reaped.
	reused := &session.SessionState{
		SessionID: sid,
		State:     session.StateWorking,
		UpdatedAt: time.Now().Unix(),
		Metrics:   &session.SessionMetrics{LastEventType: "assistant", HasOpenToolCall: true},
	}
	if !d.retainAsProcessDeath(reused, 4242, "test: pid exited") {
		t.Errorf("a mid-turn death under a recycled session id was reaped instead of converted " +
			"to error — the stale verdict's expiry branch swallowed it")
	}
}
