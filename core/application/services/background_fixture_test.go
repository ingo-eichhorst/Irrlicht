package services_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"irrlicht/core/application/services"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/session"
)

// bgOutputFixture is the setup every background-output liveness test shares: a
// `working` session whose turn has ended and whose only open background process
// is an output file, wired to a detector with an injectable probe.
type bgOutputFixture struct {
	det     *services.SessionDetector
	tw      *mockAgentWatcher
	metrics *funcMetrics
	rec     *mockRecorder
	probes  atomic.Int32
	sid     string
	path    string
}

// newBGOutputFixture builds the fixture with probe as the injected liveness
// verdict; every call is counted so a test can require positive evidence the
// probe actually ran rather than passing on the mere absence of an effect.
func newBGOutputFixture(sid, path, outPath string, probe func() (alive, conclusive bool)) *bgOutputFixture {
	return newBGOutputFixtureInState(sid, path, outPath, session.StateWorking, probe)
}

// newBGOutputFixtureInState is newBGOutputFixture with the session's starting
// state chosen explicitly — the ready-start case covers a background process
// spawned right after the session had already settled (#937).
func newBGOutputFixtureInState(sid, path, outPath, state string, probe func() (alive, conclusive bool)) *bgOutputFixture {
	f := &bgOutputFixture{sid: sid, path: path}
	f.metrics = &funcMetrics{fn: func(_, _ string) (*session.SessionMetrics, error) {
		return &session.SessionMetrics{
			LastEventType:            "turn_done",
			BackgroundProcessCount:   1,
			BackgroundProcessOutputs: []string{outPath},
		}, nil
	}}
	f.tw = newMockAgentWatcher()
	repo := newMockRepo()
	repo.states[sid] = &session.SessionState{
		SessionID:      sid,
		State:          state,
		TranscriptPath: path,
		FirstSeen:      time.Now().Unix(),
		UpdatedAt:      time.Now().Unix(),
	}
	f.det = newDetectorWithMetrics(f.tw, newMockProcessWatcher(), repo, f.metrics)
	f.rec = &mockRecorder{}
	f.det.SetRecorder(f.rec)
	f.det.SetBackgroundProbeForTest(func([]string) (bool, bool) {
		f.probes.Add(1)
		return probe()
	})
	return f
}

// start runs the detector and stops it when the test ends.
func (f *bgOutputFixture) start(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.det.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
}

// activity drives one re-evaluation. terminal short-circuits the 2s debounce,
// the way production's periodic re-probe does.
func (f *bgOutputFixture) activity(terminal bool) {
	f.tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      f.sid,
		ProjectDir:     "-Users-test",
		TranscriptPath: f.path,
		Terminal:       terminal,
	}
}

// awaitProbe blocks until the injected probe has run at least once, failing if
// it never does — without it a test asserting only that nothing happened would
// pass just as happily when the probe was never launched.
func (f *bgOutputFixture) awaitProbe(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for f.probes.Load() == 0 {
		if !time.Now().Before(deadline) {
			t.Fatal("background liveness probe was never invoked; the test would prove nothing")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
