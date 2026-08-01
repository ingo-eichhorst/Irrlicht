package services_test

import (
	"context"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"irrlicht/core/application/services"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/lifecycle"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/inbound"
)

// readyTransitions counts recorded working/waiting→ready transitions for sid.
// Asserting on the thread-safe recorder (rather than reading the repo's shared
// SessionState pointer) avoids racing with the off-loop liveness probe, which
// nudges processActivity to mutate state concurrently.
func readyTransitions(rec *mockRecorder, sid string) int {
	n := 0
	for _, ev := range rec.snapshot() {
		if ev.Kind == lifecycle.KindStateTransition && ev.SessionID == sid && ev.NewState == session.StateReady {
			n++
		}
	}
	return n
}

// waitForReadyTransition polls the recorder until a →ready transition for sid
// is observed, or fails at the deadline.
func waitForReadyTransition(t *testing.T, rec *mockRecorder, sid string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if readyTransitions(rec, sid) > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("session %s: no →ready transition recorded within deadline", sid)
}

// ClassifyState must hold a session `working` when the liveness probe has
// confirmed a background process is still alive, even though the turn ended.
// See issue #445.
func TestClassifyState_HeldByLiveBackgroundProcess(t *testing.T) {
	live := &session.SessionMetrics{LastEventType: "turn_done", HasLiveBackgroundProcess: true}
	if got, _ := services.ClassifyState(session.StateWorking, live); got != session.StateWorking {
		t.Errorf("with live background process: got %q, want working", got)
	}
	dead := &session.SessionMetrics{LastEventType: "turn_done", HasLiveBackgroundProcess: false}
	if got, _ := services.ClassifyState(session.StateWorking, dead); got != session.StateReady {
		t.Errorf("with no live background process: got %q, want ready", got)
	}
}

// End-to-end through the detector: a working session whose transcript shows an
// open background process stays working while the probe reports it alive, and
// flips to ready once the probe reports it gone — the path the 5s
// refreshStaleSessions ticker exercises in production. See issue #445.
func TestSessionDetector_BackgroundProcess_HoldsWorkingThenReady(t *testing.T) {
	// probeLive is read from the probe goroutine, so guard it with atomic.
	var probeLive atomic.Bool
	probeLive.Store(true)
	f := newBGOutputFixture(
		"bg1",
		"/home/.claude/projects/-Users-test/bg1.jsonl",
		"/tmp/x/tasks/bc1h56v8v.output",
		func() (bool, bool) { return probeLive.Load(), true },
	)
	f.start(t)

	// Probe reports the process alive → session stays working: no →ready
	// transition should be recorded even though the turn ended (turn_done).
	f.activity(false)
	f.awaitProbe(t)
	time.Sleep(250 * time.Millisecond) // allow any self-trigger to settle
	if n := readyTransitions(f.rec, f.sid); n != 0 {
		t.Fatalf("session flipped to ready %d time(s) while background process is alive", n)
	}

	// Background process exits — probe now reports it gone → session goes ready.
	probeLive.Store(false)
	f.activity(true)
	waitForReadyTransition(t, f.rec, f.sid)
}

// A session already `ready` (e.g. a prior background job just finished and
// settled it) that then shows a *fresh* background spawn on the very next
// activity event must be pulled back to working and held there while that new
// process is alive — not skip the liveness probe because the pass started
// from `ready`. Reproduces issue #937: a retried `git push -u
// run_in_background:true` launched right after the session had settled ready
// was silently invisible to the liveness gate, and the session bounced
// ready→working→ready in the same pass despite the push still running.
func TestSessionDetector_BackgroundProcess_FreshSpawnFromReady_HoldsWorking(t *testing.T) {
	var probeLive atomic.Bool
	probeLive.Store(true)
	f := newBGOutputFixtureInState(
		"bg3",
		"/home/.claude/projects/-Users-test/bg3.jsonl",
		"/tmp/x/tasks/retry.output",
		session.StateReady,
		func() (bool, bool) { return probeLive.Load(), true },
	)
	f.start(t)

	f.activity(false)
	f.awaitProbe(t)
	time.Sleep(250 * time.Millisecond) // allow any self-trigger to settle
	if n := readyTransitions(f.rec, f.sid); n != 0 {
		t.Fatalf("session flipped to ready %d time(s) while its freshly-spawned background process is alive", n)
	}

	// Background process exits — probe now reports it gone → session goes ready.
	probeLive.Store(false)
	f.activity(true)
	waitForReadyTransition(t, f.rec, f.sid)
}

// The ready→working force-bounce (see forceReadyToWorkingIfActive) must be
// logged via LogInfo, not just recorded to the lifecycle trace — otherwise it
// changes session state with zero trace in the daemon's human-readable log.
// See issue #953.
func TestSessionDetector_ForceReadyToWorking_LogsTransition(t *testing.T) {
	const sid = "force-log"
	const path = "/home/.claude/projects/-Users-test/force-log.jsonl"

	metrics := &funcMetrics{fn: func(_, _ string) (*session.SessionMetrics, error) {
		return &session.SessionMetrics{LastEventType: "turn_done"}, nil
	}}

	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()
	repo.states[sid] = &session.SessionState{
		SessionID:      sid,
		State:          session.StateReady,
		TranscriptPath: path,
		FirstSeen:      time.Now().Unix(),
		UpdatedAt:      time.Now().Unix(),
	}

	log := &mockLogger{}
	deps := defaultSessionDetectorDeps(pw, repo, nil)
	deps.Log = log
	deps.Metrics = metrics
	det := services.NewSessionDetector([]inbound.Watcher{tw}, deps)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()
	defer func() { <-done }()
	defer cancel()

	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      sid,
		ProjectDir:     "-Users-test",
		TranscriptPath: path,
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if slices.Contains(log.infoSnapshot(), services.ForceReadyToWorkingReason) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("session %s: force ready→working bounce was never logged via LogInfo", sid)
}

// A dead probe verdict must also purge the processes from the tailer's open
// set and ledger — they died without a transcript-observable termination, and
// the persisted entries would otherwise resurrect as phantom open processes
// on every daemon restart. See issue #649.
func TestSessionDetector_BackgroundProcess_DeadVerdictPurgesLedger(t *testing.T) {
	f := newBGOutputFixture(
		"bg2",
		"/home/.claude/projects/-Users-test/bg2.jsonl",
		"/tmp/x/tasks/bbw7rzpa0.output",
		func() (bool, bool) { return false, true },
	)
	f.start(t)
	f.activity(false)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if purged := f.metrics.purgedSnapshot(); len(purged) > 0 {
			if purged[0] != f.path {
				t.Errorf("purged transcript = %q, want %q", purged[0], f.path)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("dead probe verdict did not trigger PurgeDeadBackgroundProcs within deadline")
}

// An INCONCLUSIVE probe verdict must not purge. lsof walks every process on
// the machine, so it is slowest exactly when the machine is busiest — which is
// when long-running background work is most likely to be in flight. Because
// the purge writes through to the ledger, reading one slow lsof as death would
// permanently drop a still-running process and flip a live session to ready
// with no way back. The probe says "I don't know"; the detector must wait for
// the next one rather than act. See issue #1299.
func TestSessionDetector_BackgroundProcess_InconclusiveVerdictDoesNotPurge(t *testing.T) {
	// Not alive, and not conclusive — an lsof that timed out.
	f := newBGOutputFixture(
		"bg-inconclusive",
		"/home/.claude/projects/-Users-test/bg-inconclusive.jsonl",
		"/tmp/x/tasks/inconclusive.output",
		func() (bool, bool) { return false, false },
	)
	f.start(t)
	f.activity(false)
	f.awaitProbe(t)

	// The dead-verdict path purges within ~50ms of the probe returning, so a
	// short tail is enough to catch a regression here.
	time.Sleep(300 * time.Millisecond)
	if purged := f.metrics.purgedSnapshot(); len(purged) > 0 {
		t.Fatalf("an inconclusive probe purged %v — a timeout is not evidence of death", purged)
	}
}

// Skipping the purge is only half of it: an inconclusive verdict must also
// leave the cached liveness answer alone. Writing "dead" into it would flip the
// session to ready anyway — the user-visible half of the same bug — so the last
// real verdict is held until a probe actually finds out. See issue #1299.
func TestSessionDetector_BackgroundProcess_InconclusiveVerdictHoldsWorking(t *testing.T) {
	// conclusive=false throughout: every probe times out. This test drives
	// fewer probes than maxInconclusiveBackgroundProbes, so the hold is still
	// in force when the assertion runs.
	f := newBGOutputFixture(
		"bg-inconclusive-hold",
		"/home/.claude/projects/-Users-test/bg-inconclusive-hold.jsonl",
		"/tmp/x/tasks/hold.output",
		func() (bool, bool) { return false, false },
	)
	f.start(t)
	f.activity(false)
	f.awaitProbe(t)
	time.Sleep(250 * time.Millisecond)
	f.activity(true)
	time.Sleep(250 * time.Millisecond)

	if n := readyTransitions(f.rec, f.sid); n != 0 {
		t.Fatalf("session flipped to ready %d time(s) on an inconclusive probe; a timeout is not evidence the background process died", n)
	}
}

// Holding on inconclusive must be BOUNDED. "Timed out" is not proof of
// transience — a permanently oversized process table, or a hung mount lsof
// blocks on, makes every probe time out forever. Unbounded holding would pin
// the session working for the daemon's lifetime, which is the failure the
// pre-#1299 always-believe-the-silence behavior avoided. After
// maxInconclusiveBackgroundProbes the dead verdict is accepted and the purge
// runs. See issue #1299.
func TestSessionDetector_BackgroundProcess_InconclusiveHoldIsBounded(t *testing.T) {
	f := newBGOutputFixture(
		"bg-inconclusive-bound",
		"/home/.claude/projects/-Users-test/bg-inconclusive-bound.jsonl",
		"/tmp/x/tasks/bounded.output",
		func() (bool, bool) { return false, false },
	)
	f.start(t)

	// Drive more probes than the bound allows; Terminal short-circuits the
	// debounce so each event yields its own probe.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		f.activity(true)
		if purged := f.metrics.purgedSnapshot(); len(purged) > 0 {
			return // the bound released the hold, as it must
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the inconclusive hold never released across 5s of back-to-back probes; a chronically slow lsof would pin this session working forever")
}

// End-to-end through the detector, PID-liveness path (Gemini CLI writes no
// output file — only a PID): a working session whose transcript shows an open
// background PID stays working while the PID probe reports it alive, and flips
// to ready once the probe reports the PID gone. See issue #661.
func TestSessionDetector_BackgroundPID_HoldsWorkingThenReady(t *testing.T) {
	const sid = "bgpid1"
	const path = "/home/.gemini/projects/-Users-test/bgpid1.jsonl"

	// Gemini metrics: finished turn, one open background process carried by PID
	// with NO output file — the output-writer probe has nothing to inspect.
	metrics := &funcMetrics{fn: func(_, _ string) (*session.SessionMetrics, error) {
		return &session.SessionMetrics{
			LastEventType:          "turn_done",
			BackgroundProcessCount: 1,
			BackgroundProcessPIDs:  []string{"33701"},
		}, nil
	}}

	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()
	repo.states[sid] = &session.SessionState{
		SessionID:      sid,
		State:          session.StateWorking,
		TranscriptPath: path,
		FirstSeen:      time.Now().Unix(),
		UpdatedAt:      time.Now().Unix(),
	}

	det := newDetectorWithMetrics(tw, pw, repo, metrics)
	rec := &mockRecorder{}
	det.SetRecorder(rec)

	var pidLive atomic.Bool
	pidLive.Store(true)
	det.SetBackgroundPIDProbeForTest(func(pids []string) bool {
		return pidLive.Load()
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	activity := func(terminal bool) {
		tw.ch <- agent.Event{
			Type:           agent.EventActivity,
			SessionID:      sid,
			ProjectDir:     "-Users-test",
			TranscriptPath: path,
			Terminal:       terminal,
		}
	}

	// PID alive → session stays working despite turn_done.
	activity(false)
	time.Sleep(250 * time.Millisecond)
	if n := readyTransitions(rec, sid); n != 0 {
		t.Fatalf("session flipped to ready %d time(s) while background PID is alive", n)
	}

	// PID exits → probe reports it gone → session goes ready.
	pidLive.Store(false)
	activity(true)
	waitForReadyTransition(t, rec, sid)

	cancel()
	<-done
}

// A dead PID verdict must purge the PID from the tailer's open set and ledger,
// mirroring the output-file path — Gemini writes no transcript termination, so
// the entry would otherwise resurrect as a phantom open process. See issue #661.
func TestSessionDetector_BackgroundPID_DeadVerdictPurgesLedger(t *testing.T) {
	const sid = "bgpid2"
	const path = "/home/.gemini/projects/-Users-test/bgpid2.jsonl"

	metrics := &funcMetrics{fn: func(_, _ string) (*session.SessionMetrics, error) {
		return &session.SessionMetrics{
			LastEventType:          "turn_done",
			BackgroundProcessCount: 1,
			BackgroundProcessPIDs:  []string{"33701"},
		}, nil
	}}

	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()
	repo.states[sid] = &session.SessionState{
		SessionID:      sid,
		State:          session.StateWorking,
		TranscriptPath: path,
		FirstSeen:      time.Now().Unix(),
		UpdatedAt:      time.Now().Unix(),
	}

	det := newDetectorWithMetrics(tw, pw, repo, metrics)
	det.SetBackgroundPIDProbeForTest(func(pids []string) bool { return false })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      sid,
		ProjectDir:     "-Users-test",
		TranscriptPath: path,
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if purged := metrics.purgedSnapshot(); len(purged) > 0 {
			if purged[0] != path {
				t.Errorf("purged transcript = %q, want %q", purged[0], path)
			}
			cancel()
			<-done
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	t.Fatal("dead PID verdict did not trigger PurgeDeadBackgroundPIDs within deadline")
}

// A session can carry BOTH an output-file background process (Claude-Code
// shape) AND a PID background process (Gemini shape) at once. The probe holds
// the session `working` while EITHER is alive (`live = outputProbe || pidProbe`)
// and purges dead outputs and dead PIDs INDEPENDENTLY — a still-live one of
// either kind must survive the other's purge. See issue #661.
func TestSessionDetector_BackgroundMixed_IndependentLivenessAndPurge(t *testing.T) {
	const (
		outPath = "/tmp/x/tasks/bmix.output"
		pid     = "33701"
	)

	// runMixed boots a working session carrying both kinds, sets each probe to
	// the requested liveness, drives one activity pass, and returns once the
	// probe has settled (a →ready transition observed, or the grace window
	// elapsed with the session still held working).
	runMixed := func(t *testing.T, outAlive, pidAlive bool) (rec *mockRecorder, metrics *funcMetrics, path string) {
		t.Helper()
		sid := "bgmix"
		path = "/home/.gemini/projects/-Users-test/bgmix.jsonl"
		metrics = &funcMetrics{fn: func(_, _ string) (*session.SessionMetrics, error) {
			return &session.SessionMetrics{
				LastEventType:            "turn_done",
				BackgroundProcessCount:   2,
				BackgroundProcessOutputs: []string{outPath},
				BackgroundProcessPIDs:    []string{pid},
			}, nil
		}}

		tw := newMockAgentWatcher()
		pw := newMockProcessWatcher()
		repo := newMockRepo()
		repo.states[sid] = &session.SessionState{
			SessionID:      sid,
			State:          session.StateWorking,
			TranscriptPath: path,
			FirstSeen:      time.Now().Unix(),
			UpdatedAt:      time.Now().Unix(),
		}

		det := newDetectorWithMetrics(tw, pw, repo, metrics)
		rec = &mockRecorder{}
		det.SetRecorder(rec)
		det.SetBackgroundProbeForTest(func([]string) (bool, bool) { return outAlive, true })
		det.SetBackgroundPIDProbeForTest(func([]string) bool { return pidAlive })

		// godre:S8188 wants cancel deferred here, but this helper is invoked
		// from inside each t.Run subtest below and returns before the
		// subtest's assertions run — a bare `defer` at this scope would
		// cancel det.Run mid-subtest. t.Cleanup(subtest t) is the correct
		// mechanism: it defers cancel+drain to the END of whichever subtest
		// called runMixed, not to this helper's own return.
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- det.Run(ctx) }()
		t.Cleanup(func() { cancel(); <-done })

		tw.ch <- agent.Event{
			Type:           agent.EventActivity,
			SessionID:      sid,
			ProjectDir:     "-Users-test",
			TranscriptPath: path,
			Terminal:       true, // bypass debounce so the probe fires promptly
		}
		// Wait for the probe to run: a purge call is logged on every dead
		// verdict (at least one kind is dead in each sub-case below).
		waitForCondition(func() bool { return len(metrics.purgedSnapshot()) > 0 }, 2*time.Second)
		time.Sleep(150 * time.Millisecond) // let any self-trigger re-classify settle
		return rec, metrics, path
	}

	t.Run("output alive, PID dead -> held working, only PID purged", func(t *testing.T) {
		rec, metrics, path := runMixed(t, true, false)
		assertNoReadyTransition(t, rec, "bgmix", "the output process is alive")
		assertNotPurged(t, metrics.purgedProcsFor, path, "output process")
		assertPurgedExactly(t, metrics.purgedPIDsFor, purgeExpectation{Path: path, SingularNoun: "PID", PluralNoun: "PIDs", Want: pid})
	})

	t.Run("output dead, PID alive -> held working, only output purged", func(t *testing.T) {
		rec, metrics, path := runMixed(t, false, true)
		assertNoReadyTransition(t, rec, "bgmix", "the PID process is alive")
		assertNotPurged(t, metrics.purgedPIDsFor, path, "PID")
		assertPurgedExactly(t, metrics.purgedProcsFor, purgeExpectation{Path: path, SingularNoun: "output process", PluralNoun: "outputs", Want: outPath})
	})

	t.Run("both dead -> settles ready, both purged", func(t *testing.T) {
		rec, metrics, path := runMixed(t, false, false)
		waitForReadyTransition(t, rec, "bgmix")
		assertPurgedExactly(t, metrics.purgedProcsFor, purgeExpectation{Path: path, SingularNoun: "output process", PluralNoun: "outputs", Want: outPath})
		assertPurgedExactly(t, metrics.purgedPIDsFor, purgeExpectation{Path: path, SingularNoun: "PID", PluralNoun: "PIDs", Want: pid})
	})
}

// purgeQuery mirrors funcMetrics' purgedProcsFor/purgedPIDsFor query methods.
type purgeQuery func(transcriptPath string) ([]string, bool)

// assertNoReadyTransition fails t when sessionID transitioned to ready while
// becauseAlive describes why it should have been held.
func assertNoReadyTransition(t *testing.T, rec *mockRecorder, sessionID, becauseAlive string) {
	t.Helper()
	if n := readyTransitions(rec, sessionID); n != 0 {
		t.Fatalf("session flipped to ready %d time(s) while %s", n, becauseAlive)
	}
}

// assertNotPurged fails t if query reports path was purged — used for the
// still-alive half of a mixed liveness pair.
func assertNotPurged(t *testing.T, query purgeQuery, path, noun string) {
	t.Helper()
	if _, called := query(path); called {
		t.Errorf("the live %s must not be purged", noun)
	}
}

// purgeExpectation bundles assertPurgedExactly's expected-purge fields,
// keeping its parameter list within CodeScene's argument-count limit
// instead of threading each field through individually.
type purgeExpectation struct {
	Path         string
	SingularNoun string
	PluralNoun   string
	Want         string
}

// assertPurgedExactly fails t unless query reports exp.Path was purged with
// exactly [exp.Want] — used for the dead half of a mixed liveness pair.
func assertPurgedExactly(t *testing.T, query purgeQuery, exp purgeExpectation) {
	t.Helper()
	got, called := query(exp.Path)
	if !called {
		t.Fatalf("the dead %s must be purged", exp.SingularNoun)
	}
	if len(got) != 1 || got[0] != exp.Want {
		t.Errorf("purged %s = %v, want [%s]", exp.PluralNoun, got, exp.Want)
	}
}
