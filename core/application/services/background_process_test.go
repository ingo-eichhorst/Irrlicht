package services_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"irrlicht/core/application/services"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/lifecycle"
	"irrlicht/core/domain/session"
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
	const sid = "bg1"
	const path = "/home/.claude/projects/-Users-test/bg1.jsonl"

	// Metrics always show a finished turn with one open background process.
	metrics := &funcMetrics{fn: func(_, _ string) (*session.SessionMetrics, error) {
		return &session.SessionMetrics{
			LastEventType:            "turn_done",
			BackgroundProcessCount:   1,
			BackgroundProcessOutputs: []string{"/tmp/x/tasks/bc1h56v8v.output"},
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

	// probeLive is read from the probe goroutine, so guard it with atomic.
	var probeLive atomic.Bool
	probeLive.Store(true)
	det.SetBackgroundProbeForTest(func(paths []string) bool {
		return probeLive.Load()
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	// activity drives one re-evaluation. The first event for a session fires
	// processActivity immediately; later events within the 2s debounce window
	// coalesce, so we mark the follow-up Terminal to short-circuit the
	// debounce (production's periodic re-probe bypasses debounce the same way
	// via processActivityWithoutIdentity).
	activity := func(terminal bool) {
		tw.ch <- agent.Event{
			Type:           agent.EventActivity,
			SessionID:      sid,
			ProjectDir:     "-Users-test",
			TranscriptPath: path,
			Terminal:       terminal,
		}
	}

	// Probe reports the process alive → session stays working: no →ready
	// transition should be recorded even though the turn ended (turn_done).
	activity(false)
	time.Sleep(250 * time.Millisecond) // allow the async probe + any self-trigger to settle
	if n := readyTransitions(rec, sid); n != 0 {
		t.Fatalf("session flipped to ready %d time(s) while background process is alive", n)
	}

	// Background process exits — probe now reports it gone → session goes ready.
	probeLive.Store(false)
	activity(true)
	waitForReadyTransition(t, rec, sid)

	cancel()
	<-done
}

// A dead probe verdict must also purge the processes from the tailer's open
// set and ledger — they died without a transcript-observable termination, and
// the persisted entries would otherwise resurrect as phantom open processes
// on every daemon restart. See issue #649.
func TestSessionDetector_BackgroundProcess_DeadVerdictPurgesLedger(t *testing.T) {
	const sid = "bg2"
	const path = "/home/.claude/projects/-Users-test/bg2.jsonl"

	metrics := &funcMetrics{fn: func(_, _ string) (*session.SessionMetrics, error) {
		return &session.SessionMetrics{
			LastEventType:            "turn_done",
			BackgroundProcessCount:   1,
			BackgroundProcessOutputs: []string{"/tmp/x/tasks/bbw7rzpa0.output"},
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
	det.SetBackgroundProbeForTest(func(paths []string) bool { return false })

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
	t.Fatal("dead probe verdict did not trigger PurgeDeadBackgroundProcs within deadline")
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
		det.SetBackgroundProbeForTest(func([]string) bool { return outAlive })
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
		assertPurgedExactly(t, metrics.purgedPIDsFor, path, "PID", "PIDs", pid)
	})

	t.Run("output dead, PID alive -> held working, only output purged", func(t *testing.T) {
		rec, metrics, path := runMixed(t, false, true)
		assertNoReadyTransition(t, rec, "bgmix", "the PID process is alive")
		assertNotPurged(t, metrics.purgedPIDsFor, path, "PID")
		assertPurgedExactly(t, metrics.purgedProcsFor, path, "output process", "outputs", outPath)
	})

	t.Run("both dead -> settles ready, both purged", func(t *testing.T) {
		rec, metrics, path := runMixed(t, false, false)
		waitForReadyTransition(t, rec, "bgmix")
		assertPurgedExactly(t, metrics.purgedProcsFor, path, "output process", "outputs", outPath)
		assertPurgedExactly(t, metrics.purgedPIDsFor, path, "PID", "PIDs", pid)
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

// assertPurgedExactly fails t unless query reports path was purged with
// exactly [want] — used for the dead half of a mixed liveness pair.
func assertPurgedExactly(t *testing.T, query purgeQuery, path, singularNoun, pluralNoun, want string) {
	t.Helper()
	got, called := query(path)
	if !called {
		t.Fatalf("the dead %s must be purged", singularNoun)
	}
	if len(got) != 1 || got[0] != want {
		t.Errorf("purged %s = %v, want [%s]", pluralNoun, got, want)
	}
}
