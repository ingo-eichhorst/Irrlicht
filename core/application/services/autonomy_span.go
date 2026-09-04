package services

import (
	"time"

	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// Autonomy span capture (#1905).
//
// A span is one unbroken stretch of `working`; it is opened and closed on the
// state transition itself, in applyStateTransition — the single site where a
// transition and its per-target-state side effects are applied, and where
// WaitingStartTime is already stamped. Nothing here reads the lifecycle
// recordings: those are opt-in and the packaged app runs without them, so a
// recordings-derived span log would be empty for a normal user while looking
// exactly like "you never ran anything".

// AutonomySpanGrace is how long a session may sit in `ready` before the span
// it was running is treated as finished. A `working → ready → working` round
// trip completed inside this window is TOOL-CALL FLICKER — the turn boundary
// an adapter reports between two halves of the same run — and does NOT close
// the span.
//
// `working → waiting` and `working → error` get NO grace, deliberately, and
// this is the asymmetry worth stating (#1905 design decision 1):
//
//	A grace on `waiting` would merge "it asked me a question and I answered in
//	four seconds" into ONE long span. That is not a slightly worse number, it
//	is a fabricated one — the feature exists to measure how long the agent
//	went WITHOUT needing a human, and the question it asked is precisely the
//	end of that stretch. The issue's own framing applies: a wrong number here
//	is worse than no number, because nothing on screen would say it was wrong.
//	`error` is the same case (see the trade-off on closeAutonomySpan).
//
// Ten seconds, not five: it has to cover a slow tool round trip without
// covering a human. The shortest plausible human answer to a permission prompt
// is a keystroke, but the prompt itself does not arrive as `ready`, so nothing
// in the human path is at risk from a ceiling this low.
const AutonomySpanGrace = 10 * time.Second

// autonomySpanGraceSeconds is AutonomySpanGrace in the unit the session's
// timestamps are in.
const autonomySpanGraceSeconds = int64(AutonomySpanGrace / time.Second)

// applyAutonomySpanTransition opens, holds, or closes the session's autonomy
// span for a state transition that has already been decided. Called from
// applyStateTransition with the same `now` that stamps the rest of the
// transition, so a span's edges line up exactly with the transition that
// caused them.
func (d *SessionDetector) applyAutonomySpanTransition(state *session.SessionState, newState string, now int64) {
	switch newState {
	case session.StateWorking:
		d.openOrResumeAutonomySpan(state, now)
	case session.StateReady:
		// Provisional: the grace decides whether this is the end of a run or
		// a flicker between two halves of one. Nothing is written yet.
		if state.AutonomySpanStart == nil {
			return
		}
		end := now
		state.AutonomySpanPendingEnd = &end
	default:
		// Everything else is a state the session left `working` FOR, with no
		// grace — see AutonomySpanGrace. Named as `default` rather than by
		// listing the remaining states so a fifth state ends a span under its
		// own name instead of silently extending one.
		d.settleAutonomySpanOnLeave(state, newState, now)
	}
}

// openOrResumeAutonomySpan handles the `→ working` edge: it resumes a span
// held open by the flicker grace, and otherwise starts a fresh one (settling
// whatever was pending first).
func (d *SessionDetector) openOrResumeAutonomySpan(state *session.SessionState, now int64) {
	if state.AutonomySpanStart != nil && state.AutonomySpanPendingEnd != nil {
		if now-*state.AutonomySpanPendingEnd < autonomySpanGraceSeconds {
			// Flicker: the same run continues, and its start is unchanged.
			state.AutonomySpanPendingEnd = nil
			return
		}
		d.closeAutonomySpan(state, *state.AutonomySpanPendingEnd, session.StateReady)
	}
	start := now
	state.AutonomySpanStart = &start
	state.AutonomySpanPendingEnd = nil
}

// settleAutonomySpanOnLeave closes the span for a transition into a state that
// gets no grace. A pending `ready` close still WINS: the span ended when the
// session left `working`, which was to `ready`; whatever happened afterwards
// is a different stretch of the session's life.
func (d *SessionDetector) settleAutonomySpanOnLeave(state *session.SessionState, newState string, now int64) {
	if state.AutonomySpanPendingEnd != nil {
		d.closeAutonomySpan(state, *state.AutonomySpanPendingEnd, session.StateReady)
		return
	}
	if state.AutonomySpanStart == nil {
		return
	}
	d.closeAutonomySpan(state, now, newState)
}

// flushExpiredAutonomySpan writes out a pending `ready` close whose grace has
// run out, and reports whether it changed the session (so the caller can
// persist it).
//
// The event loop's 5s refresh ticker calls this for every session, which is
// what makes the grace a real ceiling rather than a promise: without it, a run
// that ended and was never resumed would sit pending forever and never be
// recorded at all — and "finished the turn and stopped" is the single most
// common way a span ends.
func (d *SessionDetector) flushExpiredAutonomySpan(state *session.SessionState, now int64) bool {
	if state == nil || state.AutonomySpanPendingEnd == nil {
		return false
	}
	if now-*state.AutonomySpanPendingEnd < autonomySpanGraceSeconds {
		return false
	}
	d.closeAutonomySpan(state, *state.AutonomySpanPendingEnd, session.StateReady)
	return true
}

// settleAutonomySpanOnTeardown finalizes a still-pending span for a session
// that is going away. A deleted session can never return to `working`, so the
// grace has nothing left to decide and the pending close is written
// immediately rather than waiting for a ticker that will never see this
// session again.
func (d *SessionDetector) settleAutonomySpanOnTeardown(state *session.SessionState) {
	if state == nil || state.AutonomySpanPendingEnd == nil {
		return
	}
	d.closeAutonomySpan(state, *state.AutonomySpanPendingEnd, session.StateReady)
}

// closeAutonomySpan clears the open span off the session and appends the
// closed span to the store. Clearing happens even with no store wired, so a
// daemon running without one cannot accumulate an ever-growing "open since"
// that the next transition would report as a multi-day run.
//
// TRADE-OFF, recorded here because this is where it is made (#1905 design
// decision 3): `error` ENDS a span rather than pausing it. A usage limit that
// stalls an agent for ten minutes and then resumes therefore shows up as two
// runs, not one — which under-reports that agent's autonomy. The alternative
// under-reports far worse: treating `error` as a pause means an agent that
// broke at 09:00 and was restarted by hand at 17:00 records a single eight-
// hour "autonomous run" that a human spent the afternoon rescuing. The reason
// column keeps the first case legible (two adjacent spans, the first ending in
// `error`); nothing could make the second case legible after the fact.
func (d *SessionDetector) closeAutonomySpan(state *session.SessionState, end int64, reason string) {
	start := state.AutonomySpanStart
	state.AutonomySpanStart = nil
	state.AutonomySpanPendingEnd = nil
	if start == nil || d.autonomySpans == nil {
		return
	}
	span := outbound.AutonomySpan{
		Start:   *start,
		End:     end,
		Project: state.ProjectName,
		Session: state.SessionID,
		Adapter: state.Adapter,
		Model:   autonomySpanModel(state),
		Reason:  reason,
	}
	if err := d.autonomySpans.RecordSpan(span); err != nil {
		d.log.LogError(logComponentAutonomySpans, state.SessionID, err.Error())
	}
}

// logComponentAutonomySpans is the log component for span-capture failures —
// never propagated, per the repo's Logger convention.
const logComponentAutonomySpans = "autonomy-spans"

// autonomySpanModel resolves the model to stamp on a span: the live
// Metrics.ModelName when the tailer has one, else the session's own Model
// field. Same resolution the cost log's rows use, so the two logs attribute a
// session to the same model.
func autonomySpanModel(state *session.SessionState) string {
	if state.Metrics != nil && state.Metrics.ModelName != "" && state.Metrics.ModelName != "unknown" {
		return state.Metrics.ModelName
	}
	return state.Model
}
