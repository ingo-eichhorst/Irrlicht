package services

import (
	"testing"
	"time"

	"irrlicht/core/domain/session"
)

// holdT0 is the fixed instant this package's tests hold signals at and
// classify at. Passing the same value for both keeps every hold well inside
// the one time-based policy's window, so these tests exercise the ladder
// rather than the compact timeout (which core/domain/session owns).
var holdT0 = time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)

// TestClassify_HookOutranksTranscript is AC2 stated at its simplest: when a
// hook-tier signal and a transcript-tier signal disagree about the same pass,
// the hook wins.
//
// The conflict is real and is the #1173 case: the transcript tail says the turn
// ended (agent_done → ready) while the idle_prompt hook says the user is
// actually sitting blocked at the prompt (→ waiting). Before hooks existed this
// pass produced `ready` and the badge was wrong.
func TestClassify_HookOutranksTranscript(t *testing.T) {
	m := &session.SessionMetrics{
		LastEventType:     "turn_done", // transcript tier: the turn is over → ready
		IdlePromptPending: true,        // hook tier: the user is blocked → waiting
	}

	v := ClassifyStateTiered(session.StateWorking, m)

	if v.State != session.StateWaiting {
		t.Errorf("state = %q, want waiting — the hook tier must win", v.State)
	}
	if v.Tier != session.TierHook {
		t.Errorf("tier = %v, want TierHook", v.Tier)
	}
	if v.Rule != string(session.SignalIdlePrompt) {
		t.Errorf("rule = %q, want %q", v.Rule, session.SignalIdlePrompt)
	}
}

// TestStateRules_LadderIsTierConsistent is AC2's real content: it asserts the
// ladder's arbitration is correct *as a property*, rather than asserting the
// particular sequence the rules happen to be written in.
//
// For every reachable combination of signals it finds which rule wins and
// checks that no rule further down the ladder — one that would also have fired
// on the same metrics — sits on a strictly higher tier. An edit that reorders
// the ladder so a transcript guess preempts a hook fails here.
//
// "Reachable" is load-bearing and is why the corpus is built by placing real
// holds and running SignalHolds.Overlay rather than by setting metrics fields
// directly. Two transcript-tier rules (user_blocking_tool, open_tool_stalled)
// are written *above* the hook-tier idle_prompt rule, which is only sound
// because an open tool call makes IsAgentDone false, which makes the
// idle_prompt hold stale, which means the pair can never contend. Building the
// corpus through Overlay is what tests that argument instead of assuming it: if
// a future change let the two co-occur, this test fails rather than the badge
// silently regressing.
func TestStateRules_LadderIsTierConsistent(t *testing.T) {
	for _, base := range reachableMetricFixtures(t) {
		for _, cur := range []string{session.StateWorking, session.StateWaiting, session.StateReady} {
			// Ask the real classifier who won, rather than re-walking the
			// ladder here: a private copy of the walk would keep passing if
			// ClassifyStateTiered ever gained a guard the copy didn't, and the
			// invariant test would silently stop guarding the invariant.
			winner := ClassifyStateTiered(cur, base.metrics)
			winnerIdx := ruleIndex(winner.Rule)
			if winnerIdx < 0 {
				t.Fatalf("%s: no rule fired — the ladder must be total", base.name)
			}

			for i := winnerIdx + 1; i < len(stateRules); i++ {
				if stateRules[i].when != nil && !stateRules[i].when(cur, base.metrics) {
					continue
				}
				if lower := stateRules[i].tierFor(base.metrics); lower.Outranks(winner.Tier) {
					t.Errorf("%s (current=%s): rule %q (tier %v) decided, but lower rule %q (tier %v) outranks it",
						base.name, cur, winner.Rule, winner.Tier, stateRules[i].id, lower)
				}
			}
		}
	}
}

// ruleIndex maps a rule id back to its position in the ladder.
func ruleIndex(id string) int {
	for i, r := range stateRules {
		if r.id == id {
			return i
		}
	}
	return -1
}

type metricFixture struct {
	name    string
	metrics *session.SessionMetrics
}

// reachableMetricFixtures builds the corpus for the ladder property test: every
// combination of transcript shapes crossed with every subset of held hook
// signals, each run through SignalHolds.Overlay so the resulting metrics are
// only ever ones the live detector could actually hand the classifier.
func reachableMetricFixtures(t *testing.T) []metricFixture {
	t.Helper()

	transcripts := []metricFixture{
		{"idle-turn-done", &session.SessionMetrics{LastEventType: "turn_done"}},
		{"assistant-tail", &session.SessionMetrics{LastEventType: "assistant"}},
		{"user-tail", &session.SessionMetrics{LastEventType: "user"}},
		{"esc-interrupt", &session.SessionMetrics{LastEventType: "user", LastWasUserInterrupt: true}},
		{"tool-denial", &session.SessionMetrics{LastEventType: "user", LastWasToolDenial: true}},
		{"user-blocking-tool-open", &session.SessionMetrics{
			LastEventType: "assistant", HasOpenToolCall: true,
			LastOpenToolNames: []string{"AskUserQuestion"},
		}},
		{"edit-tool-open", &session.SessionMetrics{
			LastEventType: "assistant", HasOpenToolCall: true,
			LastOpenToolNames: []string{"Edit"},
		}},
		{"live-background-process", &session.SessionMetrics{
			LastEventType: "turn_done", HasLiveBackgroundProcess: true,
		}},
		{"compacting", &session.SessionMetrics{LastEventType: "assistant", CompactInProgress: true}},
	}

	holdSets := []struct {
		name  string
		kinds []session.SignalKind
	}{
		{"no-hooks", nil},
		{"permission", []session.SignalKind{session.SignalPermissionPrompt}},
		{"turn-done", []session.SignalKind{session.SignalTurnDone}},
		{"idle-prompt", []session.SignalKind{session.SignalIdlePrompt}},
		{"turn-done+idle-prompt", []session.SignalKind{session.SignalTurnDone, session.SignalIdlePrompt}},
		{"permission+idle-prompt", []session.SignalKind{session.SignalPermissionPrompt, session.SignalIdlePrompt}},
		{"compact", []session.SignalKind{session.SignalCompactInProgress}},
		{"compact+turn-done", []session.SignalKind{session.SignalCompactInProgress, session.SignalTurnDone}},
		// The transcript-tier stalled-edit hold (#488). Before #1319 this shape
		// was faked by setting OpenToolStalled directly on a transcript fixture
		// — the very route this builder's doc comment forbids, since the flag is
		// now only ever reachable through a ripened hold. Crossing it with the
		// permission hook is the combination that would catch a reorder letting
		// the transcript fallback preempt the hook it defers to.
		{"stalled-edit", []session.SignalKind{session.SignalOpenToolStalled}},
		{"permission+stalled-edit", []session.SignalKind{
			session.SignalPermissionPrompt, session.SignalOpenToolStalled,
		}},
		{"all", []session.SignalKind{
			session.SignalPermissionPrompt, session.SignalTurnDone,
			session.SignalIdlePrompt, session.SignalCompactInProgress,
			session.SignalOpenToolStalled,
		}},
	}

	var out []metricFixture
	for _, tr := range transcripts {
		for _, hs := range holdSets {
			m := *tr.metrics // copy: Overlay mutates
			holds := session.NewSignalHolds()
			for _, k := range hs.kinds {
				holds.Hold("s", k, session.SignalPayload{}, armedAt(k))
			}
			holds.Overlay("s", &m, holdT0)
			out = append(out, metricFixture{name: tr.name + "/" + hs.name, metrics: &m})
		}
	}
	return out
}

// armedAt is when a hold of this kind must have been placed for the pass at
// holdT0 to see it in its interesting state.
//
// Every hook signal is ripe on arrival, so holdT0 is right for them — and it
// must stay holdT0 for SignalCompactInProgress, whose hold goes stale once
// compactHoldTimeout has elapsed. SignalOpenToolStalled is the one arm-then-fire
// row: armed at holdT0 it would be unripe and apply nothing, so the corpus would
// silently contain no stalled fixtures at all. An hour is deliberately far past
// any plausible stalledEditToolThreshold, which is unexported in domain/session
// and so cannot be named here.
func armedAt(kind session.SignalKind) time.Time {
	if kind == session.SignalOpenToolStalled {
		return holdT0.Add(-time.Hour)
	}
	return holdT0
}

// TestClassify_ProvenanceDistinguishesHookFromInference is AC3: two passes that
// produce the identical state AND the identical human-readable reason must
// still be tellable apart by where their evidence came from.
//
// This is the concrete gap the epic named — a recorded trace could show a
// session going ready but not whether Claude Code's Stop hook said so or a
// heuristic guessed it from the transcript tail. The Reason string is
// deliberately identical in both cases: it is pinned byte-for-byte by 338
// replay goldens, so provenance had to land somewhere other than the prose.
func TestClassify_ProvenanceDistinguishesHookFromInference(t *testing.T) {
	inferred := ClassifyStateTiered(session.StateWorking, &session.SessionMetrics{
		LastEventType: "turn_done", // transcript tail heuristic
	})
	hooked := ClassifyStateTiered(session.StateWorking, &session.SessionMetrics{
		LastEventType: "assistant_message", // tail alone would NOT say done
		HookTurnDone:  true,                // the Stop hook did
	})

	if inferred.State != session.StateReady || hooked.State != session.StateReady {
		t.Fatalf("both must reach ready: inferred=%q hooked=%q", inferred.State, hooked.State)
	}
	if inferred.Reason != hooked.Reason {
		t.Fatalf("precondition failed: the two paths must share a reason string, got %q vs %q",
			inferred.Reason, hooked.Reason)
	}

	if inferred.Tier != session.TierTranscript {
		t.Errorf("inferred ready tier = %v, want TierTranscript", inferred.Tier)
	}
	if hooked.Tier != session.TierHook {
		t.Errorf("hook-delivered ready tier = %v, want TierHook", hooked.Tier)
	}
}

// TestWithProvenance_StampsOnlyClassifierVerdicts pins that provenance is a
// property of a verdict, not of metrics: a synthesized transition (emitted by
// the collapse/catch-up synthesizers, which no rule decided) must record no
// tier rather than borrowing one.
func TestWithProvenance_StampsOnlyClassifierVerdicts(t *testing.T) {
	m := &session.SessionMetrics{LastEventType: "turn_done", HookTurnDone: true}

	stamped := withProvenance(classifierInputs(m), ClassifyStateTiered(session.StateWorking, m))
	if stamped.DecidedByTier != "hook" || stamped.DecidedByRule != "agent_done" {
		t.Errorf("classifier verdict must stamp provenance, got tier=%q rule=%q",
			stamped.DecidedByTier, stamped.DecidedByRule)
	}

	synthetic := withProvenance(classifierInputs(m), StateVerdict{})
	if synthetic.DecidedByTier != "" || synthetic.DecidedByRule != "" {
		t.Errorf("a synthesized transition must record no provenance, got tier=%q rule=%q",
			synthetic.DecidedByTier, synthetic.DecidedByRule)
	}
}

// TestClassifierInputs_CapturesHookSignals covers the two fields that were
// missing from the recorded snapshot before #1288 — without them a trace could
// not show which tier decided even when the classifier knew.
func TestClassifierInputs_CapturesHookSignals(t *testing.T) {
	in := classifierInputs(&session.SessionMetrics{HookTurnDone: true, IdlePromptPending: true})
	if !in.HookTurnDone || !in.IdlePromptPending {
		t.Errorf("hook signals must be captured in the trace snapshot: %+v", in)
	}
}

// TestClassify_LateAuthoritativeSignalDoesNotFlap is AC5, and the reason
// Phase 3 exists at all: the authoritative signal arrives *after* the
// lower-tier guess has already been shown.
//
// The measured live sequence (#1141): the turn ends on a plain statement with
// no question or cue, so the transcript tier says ready and the badge shows
// ready. ~6s later the idle_prompt hook lands saying the user is actually
// blocked. The correction must be a single clean ready→waiting, and must then
// hold across every subsequent reclassify — an fswatcher touch re-running the
// classifier must not walk it back to ready, which is what an unheld signal
// would do on the very next pass.
func TestClassify_LateAuthoritativeSignalDoesNotFlap(t *testing.T) {
	holds := session.NewSignalHolds()
	const sid = "s"

	// The turn ends; no hook has landed yet. Transcript tier decides.
	m := &session.SessionMetrics{LastEventType: "turn_done"}
	holds.Overlay(sid, m, holdT0)
	v := ClassifyStateTiered(session.StateWorking, m)
	if v.State != session.StateReady {
		t.Fatalf("before the hook lands, the transcript tier must say ready, got %q", v.State)
	}
	if v.Tier != session.TierTranscript {
		t.Fatalf("that verdict must be marked as inferred, got tier %v", v.Tier)
	}
	state := v.State

	// ~6s later: the idle_prompt hook lands and corrects the guess.
	holds.Hold(sid, session.SignalIdlePrompt, session.SignalPayload{}, holdT0)

	corrected := &session.SessionMetrics{LastEventType: "turn_done"}
	holds.Overlay(sid, corrected, holdT0)
	v = ClassifyStateTiered(state, corrected)
	if v.State != session.StateWaiting {
		t.Fatalf("the hook must correct ready→waiting, got %q", v.State)
	}
	if v.Tier != session.TierHook {
		t.Errorf("the correction must be attributed to the hook tier, got %v", v.Tier)
	}
	state = v.State

	// Every later reclassify while the user is still blocked must stay put —
	// no oscillation back through ready.
	for pass := 1; pass <= 3; pass++ {
		again := &session.SessionMetrics{LastEventType: "turn_done"}
		holds.Overlay(sid, again, holdT0)
		v = ClassifyStateTiered(state, again)
		if v.State != session.StateWaiting {
			t.Fatalf("pass %d: state flapped to %q; the held signal must keep it in waiting", pass, v.State)
		}
		if v.Reason != "" {
			t.Errorf("pass %d: a no-op re-classification must not emit a transition, got reason %q", pass, v.Reason)
		}
	}

	// The user finally replies: the hold goes stale and the session moves on.
	replied := &session.SessionMetrics{LastEventType: "user"}
	holds.Overlay(sid, replied, holdT0)
	v = ClassifyStateTiered(state, replied)
	if v.State != session.StateWorking {
		t.Errorf("after the user replies the session must resume working, got %q", v.State)
	}
}
