package services

import (
	"testing"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// journalSpanStore is a span store that also keeps the OPEN-run journal, so a
// test can hand the same store to two detectors and ask what survived between
// them — which is what a daemon restart is.
//
// It carries the journal methods even in the shape this file was first run
// against, where the shipped AutonomySpanStore had none of them: a Go value may
// hold methods beyond the interface it satisfies, so the two red-first tests
// below compiled and FAILED ON BEHAVIOUR against the unfixed daemon rather than
// failing to build. That is the difference between evidence and a claim
// (AGENTS.md, "House style for what counts as evidence").
type journalSpanStore struct {
	spans []outbound.AutonomySpan
	open  map[string]outbound.AutonomySpan
}

func newJournalSpanStore() *journalSpanStore {
	return &journalSpanStore{open: map[string]outbound.AutonomySpan{}}
}

func (j *journalSpanStore) RecordSpan(s outbound.AutonomySpan) error {
	delete(j.open, s.Session)
	if s.Project == "" || s.Duration() <= 0 {
		return nil
	}
	j.spans = append(j.spans, s)
	return nil
}

func (j *journalSpanStore) RecordOpenSpan(s outbound.AutonomySpan) error {
	j.open[s.Session] = s
	return nil
}

func (j *journalSpanStore) SyncOpenSpans(spans []outbound.AutonomySpan) error {
	j.open = make(map[string]outbound.AutonomySpan, len(spans))
	for _, s := range spans {
		j.open[s.Session] = s
	}
	return nil
}

func (j *journalSpanStore) OpenSpans() ([]outbound.AutonomySpan, error) {
	out := make([]outbound.AutonomySpan, 0, len(j.open))
	for _, s := range j.open {
		out = append(out, s)
	}
	return out, nil
}

func (j *journalSpanStore) SpansInWindow(outbound.AutonomySpanQuery) (*outbound.AutonomySpanResult, error) {
	return &outbound.AutonomySpanResult{}, nil
}

func (j *journalSpanStore) Prune(int) error { return nil }

// bornWorkingDetector builds a detector wired to store, with a metrics
// collector whose transcript already shows an open turn — the population
// shouldClassifyAtBirth births `working`.
func bornWorkingDetector(store outbound.AutonomySpanStore) *SessionDetector {
	d := NewSessionDetector(nil, SessionDetectorDeps{
		Log:     bornLog{},
		Git:     bornGit{},
		Metrics: openTurnMetrics{},
	})
	d.SetAutonomySpanStore(store)
	return d
}

// birthEvent is the discovery event both restart halves see: same session id,
// same transcript, because a restart rediscovers the SAME session.
func birthEvent() agent.Event {
	return agent.Event{SessionID: "sess-1", TranscriptPath: "/tmp/x/events.jsonl", CWD: "/tmp/x"}
}

// TestAutonomySpan_SessionBornWorkingOpensASpan is red-first defect 1 (#1905
// recording).
//
// shouldClassifyAtBirth births a session `working` when it is a child (#889) or
// when the transcript already carries evidence (#1447) — and the birth path
// assigns state.State DIRECTLY, bypassing applyStateTransition, whose single
// call to applyAutonomySpanTransition was the only place a span could open. So
// every session that was ALREADY running when Irrlicht first saw it recorded
// nothing at all, and "already running when Irrlicht first saw it" is what
// every session is after a daemon restart.
//
// Seen red before the fix: `AutonomySpanStart = <nil>`.
func TestAutonomySpan_SessionBornWorkingOpensASpan(t *testing.T) {
	const born = 1_700_000_000

	d := bornWorkingDetector(newJournalSpanStore())
	state := d.buildNewSessionState(agent.Identity{Name: "copilot"}, birthEvent(), born)

	if state.State != session.StateWorking {
		t.Fatalf("State = %q, want %q — the fixture's whole point is a session born working",
			state.State, session.StateWorking)
	}
	if state.AutonomySpanStart == nil {
		t.Fatal("AutonomySpanStart = nil: a session born `working` is a run that is " +
			"under way, and opening no span for it is how the feature lost most of its runs")
	}
	if got := *state.AutonomySpanStart; got != born {
		t.Errorf("AutonomySpanStart = %d, want %d (discovery time — nothing else is known)", got, born)
	}
}

// TestAutonomySpan_OpenRunSurvivesADaemonRestart is red-first defect 2 (#1905
// recording), and it is the one that ate the LONG runs: the longer a run, the
// likelier it crosses a restart, so the feature systematically lost precisely
// the runs it exists to report.
//
// The two halves share one store, which is what makes this a restart rather
// than two unrelated daemons: the first daemon's open run has to be readable by
// the second.
//
// Seen red before the fix: `spans recorded = 0`.
func TestAutonomySpan_OpenRunSurvivesADaemonRestart(t *testing.T) {
	const (
		startedAt   = 1_700_000_000
		restartedAt = startedAt + 3600
		finishedAt  = restartedAt + 1800
	)

	store := newJournalSpanStore()

	// Daemon 1 discovers a session that is already working, and dies.
	first := bornWorkingDetector(store)
	first.buildNewSessionState(agent.Identity{Name: "copilot"}, birthEvent(), startedAt)

	// Daemon 2 comes up and rediscovers the same session, still working.
	second := bornWorkingDetector(store)
	revived := second.buildNewSessionState(agent.Identity{Name: "copilot"}, birthEvent(), restartedAt)

	// It finishes its turn; the grace expires and the span is filed.
	second.applyAutonomySpanTransition(revived, session.StateReady, finishedAt)
	second.flushExpiredAutonomySpan(revived, finishedAt+autonomySpanGraceSeconds)

	if len(store.spans) != 1 {
		t.Fatalf("spans recorded = %d, want 1 — a run that crossed a daemon restart was lost entirely",
			len(store.spans))
	}
	got := store.spans[0]
	if got.Start != startedAt {
		t.Errorf("span.Start = %d, want %d — the restart must not restart the clock: a "+
			"90-minute run would otherwise be filed as a 30-minute one", got.Start, startedAt)
	}
	if got.End != finishedAt {
		t.Errorf("span.End = %d, want %d", got.End, finishedAt)
	}
}

// TestAutonomySpan_RunInProgressIsVisible is red-first defect 3 (#1905
// recording): a span was only ever written when it CLOSED, so a run still under
// way contributed nothing at all — the 3-hour run the maintainer was watching
// while the panel said its longest was 35 minutes.
//
// Asserted through the store's open-run journal, which is the daemon's record
// of what is running right now; the filesystem tracker's own half of this —
// serving that journal back through SpansInWindow — is
// TestSpansInWindow_ReturnsTheRunStillInProgress.
//
// Seen red before the fix: `open runs = 0`.
func TestAutonomySpan_RunInProgressIsVisible(t *testing.T) {
	const born = 1_700_000_000

	store := newJournalSpanStore()
	d := bornWorkingDetector(store)
	d.buildNewSessionState(agent.Identity{Name: "copilot"}, birthEvent(), born)

	open, err := store.OpenSpans()
	if err != nil {
		t.Fatalf("OpenSpans: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("open runs = %d, want 1 — a run that has not ended yet is still a run, "+
			"and a feature that only records finished ones cannot see the long one you are watching",
			len(open))
	}
	if open[0].Start != born {
		t.Errorf("open run Start = %d, want %d", open[0].Start, born)
	}
	if open[0].Session != "sess-1" {
		t.Errorf("open run Session = %q, want %q", open[0].Session, "sess-1")
	}
}
