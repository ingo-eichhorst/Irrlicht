package services_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"irrlicht/core/adapters/inbound/agents/muse"
	"irrlicht/core/application/replayengine"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/session"
	"irrlicht/core/pkg/tailer"
)

// museAutoReviewFixtureLines reads
// testdata/real-approval-flow-escalated.jsonl (owned by core/adapters/
// inbound/agents/muse) — requested{presentation_phase:"automated_reviewing"}
// -> automated_review_started -> automated_review_completed{status:
// "escalated"} -> decision_applied, read here across the package boundary.
// See TestClassifyState_MuseAutoReview_JudgeReviewWindowNotWaiting's doc
// comment for why that direction is allowed.
func museAutoReviewFixtureLines(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "adapters", "inbound", "agents", "muse", "testdata", "real-approval-flow-escalated.jsonl"))
	if err != nil {
		t.Fatalf("read muse fixture: %v", err)
	}
	var lines []string
	for _, l := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) != 4 {
		t.Fatalf("real-approval-flow-escalated.jsonl has %d lines, want 4 (requested/automated_review_started/automated_review_completed/decision_applied) — fixture shape changed underneath this test", len(lines))
	}
	return lines
}

// museAutoReviewClassify replays lines through a real tailer.TranscriptTailer
// + muse.Parser and returns the resulting classifier verdict, mirroring
// task_notification_continuation_test.go's fixture pattern one file over.
func museAutoReviewClassify(t *testing.T, lines []string) (string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	m, err := tailer.NewTranscriptTailer(path, &muse.Parser{}, muse.AdapterName).TailAndProcess()
	if err != nil {
		t.Fatalf("TailAndProcess: %v", err)
	}
	metrics := replayengine.TailerToDomain(m)
	return services.ClassifyState(session.StateWorking, metrics)
}

// TestClassifyState_MuseAutoReview_JudgeReviewWindowNotWaiting is issue
// #1978's user-observable defect claim, asserted at the same altitude a
// dashboard or the macOS menu bar reads: while muse's own ":auto-review" LLM
// judge is still deciding a tool approval (requested{presentation_phase:
// "automated_reviewing"} -> automated_review_started, no
// automated_review_completed yet), ClassifyState must NOT report "waiting"
// — no user is involved yet.
//
// This cannot be asserted inside core/adapters/inbound/agents/muse itself:
// that package cannot import core/application/services
// (core/architecture_test.go's hexagonal import-direction rule forbids
// application/services -> adapters/inbound/*). The reverse edge used here —
// this services package importing the muse adapter — only exists in a
// _test.go file, and architecture_test.go's own doc comment says its
// packages.Load call "runs without Tests", i.e. it inspects only non-test
// imports; core/application/services/task_notification_continuation_test.go
// already imports the claudecode adapter the same way. The muse package's
// own TestParseLine_ApprovalFlow_Escalated_ReviewWindow_NotWaiting stops one
// layer short, at TranscriptPermissionPending — this test carries the same
// claim through to the actual session state.
func TestClassifyState_MuseAutoReview_JudgeReviewWindowNotWaiting(t *testing.T) {
	lines := museAutoReviewFixtureLines(t)
	state, reason := museAutoReviewClassify(t, lines[:2]) // requested -> automated_review_started

	if state == session.StateWaiting {
		t.Errorf("ClassifyState = %q (reason %q) during the judge's own automated-review window, want anything but %q (issue #1978)",
			state, reason, session.StateWaiting)
	}
}

// TestClassifyState_MuseAutoReview_EscalationIsWaiting is the arc's other
// half, a lock: once the judge escalates (automated_review_completed{status:
// "escalated"}), the wait genuinely becomes the user's, and ClassifyState
// must report "waiting". This holds both before and after issue #1978's fix
// — pre-fix because "requested" already opened the prompt unconditionally,
// post-fix because the fix's own re-open branch
// (parseApprovalReviewCompleted) must reach the identical verdict.
func TestClassifyState_MuseAutoReview_EscalationIsWaiting(t *testing.T) {
	lines := museAutoReviewFixtureLines(t)
	state, reason := museAutoReviewClassify(t, lines[:3]) // + automated_review_completed{status:"escalated"}

	if state != session.StateWaiting {
		t.Errorf("ClassifyState = %q (reason %q) after an escalated automated review, want %q — the user genuinely must decide now",
			state, reason, session.StateWaiting)
	}
}
