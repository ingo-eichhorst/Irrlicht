package main

import (
	"sort"
	"time"

	"irrlicht/core/application/services"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// graceSeconds is the production flicker grace, in the unit the logs are
// stamped in. IMPORTED, never retyped: services.AutonomySpanGrace is the
// number the live daemon applies, and a back-fill that used a different one
// would draw a differently-shaped history for the same behaviour.
var graceSeconds = int64(services.AutonomySpanGrace / time.Second)

// costGapSeconds is how long one session's cost log may go quiet before the
// active stretch it was in is treated as finished.
//
// 180 s — three times the cost log's own 60 s write interval — and it is
// bounded from both sides by something real:
//
//   - LONGER than the write interval by a margin, because the log writes only
//     when a value CHANGED. A tool call that spends a minute without producing
//     a token writes nothing at all, and a threshold at or near 60 s would cut
//     one run into several at every such pause.
//   - SHORTER than a person, because the quantity being reconstructed is how
//     long the agent went WITHOUT needing one. Someone who reads a question
//     and answers it inside a minute must not be absorbed into the run they
//     interrupted; that would not be a slightly worse number, it would be a
//     fabricated one.
//
// The choice is not free and is not hidden: `--sensitivity` prints the span
// count and the p50/p95 this threshold produces alongside 120 s and 300 s, so
// what it costs is visible rather than asserted.
const costGapSeconds int64 = 180

// sensitivityThresholds are the gap thresholds `--sensitivity` reports, with
// costGapSeconds among them so the table always contains the value in force.
var sensitivityThresholds = []int64{120, costGapSeconds, 300}

// rawSpan is one reconstructed span before it is attributed to a project.
type rawSpan struct {
	Start   int64
	End     int64
	Session string
	Reason  string
}

// lossReport counts everything the reconstruction deliberately threw away.
//
// Every field here is a span (or a transition) that a less careful back-fill
// would have turned into a number. They are printed, not hidden: an undercount
// whose size is stated is a different thing from an undercount nobody can see.
type lossReport struct {
	// Restarts is how many distinct daemon boots the event log records.
	Restarts int

	// RestartStraddlers is spans dropped because a daemon restart falls inside
	// them. Such a span is not one run: it is several, merged because the
	// transitions between them were never logged. It is DROPPED rather than
	// split, because splitting would invent the boundaries it cannot observe.
	RestartStraddlers int

	// OrphanCloses is transitions out of `working` that arrived with no open
	// span — the close half of a run whose open half is not in the retained
	// log (most of them sit just before the oldest rotated file). No start
	// means no duration, so nothing is emitted.
	OrphanCloses int

	// OrphanReady is the same thing for a `ready` arriving with no open span.
	// Counted separately because `ready` is provisional in the production
	// rule: it never emits a span on its own, so this number is informational
	// where OrphanCloses is a real loss.
	OrphanReady int

	// ReopenedWhileOpen is `→ working` arriving while a span was already open
	// and nothing was pending. The production detector does exactly this —
	// the older start is replaced — so the back-fill matches it rather than
	// inventing a close the daemon would not have written.
	ReopenedWhileOpen int

	// UnclosedAtEnd is spans still open when the log ends. An open span has no
	// measured end, and inventing one ("it must have stopped by now") is the
	// single easiest way to manufacture a very long run.
	UnclosedAtEnd int

	// NonPositive is spans whose end did not follow their start.
	NonPositive int

	// NoProject is spans dropped for having no project to file them under. The
	// strip draws one row per project, so such a span cannot be shown; the
	// span store no-ops on them anyway.
	NoProject int

	// BoundaryStraddlers is cost-derived stretches dropped for crossing into
	// the event log's era. The two sources must not both describe the same
	// instant, and the event log is the better witness wherever it reaches.
	BoundaryStraddlers int

	// OverlapsLive is spans dropped for reaching into the era the daemon has
	// ALREADY measured.
	//
	// This is the drop that makes the tool safe to run late. The event log
	// keeps being written after the feature ships, so its era and the live
	// span log's overlap — and a run present in both would be counted twice,
	// which is the one error a back-fill can make that looks like productivity.
	// The live rows win: they were measured.
	OverlapsLive int
}

// spanBuilder replays logged transitions through the daemon's own span rules.
//
// It is a deliberate re-implementation of
// core/application/services/autonomy_span.go rather than a call into it: that
// code is a method on SessionDetector and drives a live store, while this
// walks a frozen log. The RULES are the thing that must match, and the tests
// pin them case by case — open on `working`, provisional close on `ready`, no
// grace on anything else, and a `working → ready → working` round trip inside
// the grace is flicker that does not close the span.
type spanBuilder struct {
	grace   int64
	open    map[string]int64
	pending map[string]int64
	spans   []rawSpan
	loss    lossReport
}

func newSpanBuilder(grace int64) *spanBuilder {
	return &spanBuilder{
		grace:   grace,
		open:    map[string]int64{},
		pending: map[string]int64{},
	}
}

// apply folds one transition into the builder.
func (b *spanBuilder) apply(t transition) {
	switch t.State {
	case session.StateWorking:
		b.onWorking(t)
	case session.StateReady:
		// Provisional: the grace decides whether this ends a run or sits
		// between two halves of one. Nothing is emitted yet.
		if _, ok := b.open[t.Session]; ok {
			b.pending[t.Session] = t.TS
			return
		}
		b.loss.OrphanReady++
	default:
		// Every other state is one the session left `working` FOR, with no
		// grace. Written as `default` rather than by naming the remaining
		// states, so a fifth state ends a span under its own name — the same
		// shape applyAutonomySpanTransition uses, and for the same reason.
		b.onLeave(t)
	}
}

// onWorking handles the `→ working` edge.
func (b *spanBuilder) onWorking(t transition) {
	start, isOpen := b.open[t.Session]
	pend, isPending := b.pending[t.Session]
	switch {
	case isOpen && isPending && t.TS-pend < b.grace:
		// Flicker: the same run continues, and its start is unchanged.
		delete(b.pending, t.Session)
		return
	case isOpen && isPending:
		b.emit(start, pend, t.Session, session.StateReady)
	case isOpen:
		b.loss.ReopenedWhileOpen++
	}
	b.open[t.Session] = t.TS
	delete(b.pending, t.Session)
}

// onLeave handles a transition into a state that gets no grace. A pending
// `ready` close still WINS: the span ended when the session left `working`,
// which was to `ready`.
func (b *spanBuilder) onLeave(t transition) {
	if pend, ok := b.pending[t.Session]; ok {
		b.emit(b.open[t.Session], pend, t.Session, session.StateReady)
		return
	}
	if start, ok := b.open[t.Session]; ok {
		b.emit(start, t.TS, t.Session, t.State)
		return
	}
	b.loss.OrphanCloses++
}

// emit records one closed span and clears the session's open state.
func (b *spanBuilder) emit(start, end int64, sid, reason string) {
	delete(b.open, sid)
	delete(b.pending, sid)
	if end <= start {
		b.loss.NonPositive++
		return
	}
	b.spans = append(b.spans, rawSpan{Start: start, End: end, Session: sid, Reason: reason})
}

// finish settles what is still open when the log ends.
//
// A pending `ready` older than the grace is a REAL span: the grace has already
// run out, so the live daemon's 5 s ticker would have written it. Anything
// still genuinely open is dropped — an open span has no measured end, and the
// one thing not to do is invent one.
func (b *spanBuilder) finish(now int64) {
	sessions := make([]string, 0, len(b.pending))
	for sid := range b.pending {
		sessions = append(sessions, sid)
	}
	sort.Strings(sessions) // deterministic emission order for the tests
	for _, sid := range sessions {
		pend := b.pending[sid]
		start, ok := b.open[sid]
		if ok && now-pend >= b.grace {
			b.emit(start, pend, sid, session.StateReady)
		}
	}
	b.loss.UnclosedAtEnd += len(b.open)
	b.open = map[string]int64{}
	b.pending = map[string]int64{}
}

// reconstructEventSpans replays the event log into spans carrying REAL end
// reasons, dropping every span the log cannot vouch for.
//
// projects maps a session id to the project it belongs to; the event log does
// not carry one, so it is joined in from the cost log (see sessionProjects).
func reconstructEventSpans(log *eventLog, projects map[string]string, now int64) ([]outbound.AutonomySpan, lossReport) {
	if log.Subagents == nil {
		// Unreachable on the production path — readEventLog always builds one.
		// Panicking rather than substituting an empty index is deliberate: an
		// empty index classifies EVERY run as unknown, which is exactly what a
		// machine whose log carried no parentage looks like, so the wiring bug
		// would be invisible in the output (AGENTS.md: inability to look must
		// never read the same as finding nothing).
		panic("reconstructEventSpans: event log has no subagent index")
	}
	b := newSpanBuilder(graceSeconds)
	for _, t := range log.Transitions {
		b.apply(t)
	}
	b.finish(now)

	loss := b.loss
	loss.Restarts = len(log.Restarts)

	kept := make([]outbound.AutonomySpan, 0, len(b.spans))
	for _, s := range b.spans {
		if straddlesRestart(s, log.Restarts) {
			loss.RestartStraddlers++
			continue
		}
		project := projects[s.Session]
		if project == "" {
			loss.NoProject++
			continue
		}
		kind, parent := log.Subagents.classify(s.Session)
		kept = append(kept, outbound.AutonomySpan{
			Start:   s.Start,
			End:     s.End,
			Project: project,
			Session: s.Session,
			Reason:  s.Reason,
			Source:  session.AutonomySourceLog,
			Kind:    kind,
			Parent:  parent,
		})
	}
	sortSpans(kept)
	return kept, loss
}

// straddlesRestart reports whether a daemon restart falls strictly inside the
// span.
//
// STRICTLY inside: a restart exactly at a boundary is the transition that
// opened or closed the span, not something that happened during it. Binary
// search rather than a scan — restarts are sorted, and this runs once per
// span over a list that grows with uptime.
func straddlesRestart(s rawSpan, restarts []int64) bool {
	i := sort.Search(len(restarts), func(i int) bool { return restarts[i] > s.Start })
	return i < len(restarts) && restarts[i] < s.End
}

// reconstructCostSpans rebuilds spans from the cost log's activity series, for
// the era the event log does not reach.
//
// Every span it produces carries session.AutonomyReasonUnknown, and that is
// not a placeholder to be improved later — the source records that a session
// was consuming tokens at an instant and contains nothing whatsoever about why
// it stopped. Defaulting to `ready` would paint months of the strip green
// under a "the turn finished" claim nobody measured; the duration chart, which
// is this section's main element, does not read the reason at all, so telling
// the truth here costs nothing.
//
// until bounds the era: a stretch that ends at or after it is dropped rather
// than clipped, because the event log is the better witness wherever it
// reaches and one run must not be described by both sources. since, when
// non-zero, drops everything starting before it.
// ix classifies each stretch's session as a top-level or subagent run where the
// event log can say — which, for most of this era, it cannot: the cost log
// reaches back months further than the retained event log, so a session that
// ended before the oldest retained file was written has no birth line to read
// and comes back session.AutonomyKindUnknown. That is the honest answer and the
// reason the kind has a third state; assuming "top" here would put months of
// runs into the default view under a claim nothing measured.
func reconstructCostSpans(cl *costLog, ix *subagentIndex, gap, until, since int64) ([]outbound.AutonomySpan, lossReport) {
	var loss lossReport
	out := []outbound.AutonomySpan{}
	keys := make([]sessionKey, 0, len(cl.Series))
	for k := range cl.Series {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Project != keys[j].Project {
			return keys[i].Project < keys[j].Project
		}
		return keys[i].Session < keys[j].Session
	})
	for _, k := range keys {
		for _, st := range activeStretches(cl.Series[k], gap) {
			switch {
			case st.End <= st.Start:
				loss.NonPositive++
			case until > 0 && st.End >= until:
				loss.BoundaryStraddlers++
			case since > 0 && st.Start < since:
				// Deliberately not counted as loss: an explicit --since floor
				// is the caller asking for less, not the data failing.
			default:
				kind, parent := ix.classify(k.Session)
				out = append(out, outbound.AutonomySpan{
					Start:   st.Start,
					End:     st.End,
					Project: k.Project,
					Session: k.Session,
					Reason:  session.AutonomyReasonUnknown,
					Source:  session.AutonomySourceCost,
					Kind:    kind,
					Parent:  parent,
				})
			}
		}
	}
	sortSpans(out)
	return out, loss
}

// stretch is one contiguous run of cost-log activity.
type stretch struct{ Start, End int64 }

// activeStretches splits one session's sorted timestamp series wherever it
// goes quiet for longer than gap.
//
// The stretch runs from the FIRST to the LAST row in it, so the run's true
// start — some seconds before its first token was priced — is never invented.
// That under-reports every span by up to one write interval, which is the safe
// direction and is stated in the report rather than corrected for.
func activeStretches(series []int64, gap int64) []stretch {
	if len(series) == 0 {
		return nil
	}
	var out []stretch
	start, prev := series[0], series[0]
	for _, ts := range series[1:] {
		if ts-prev > gap {
			out = append(out, stretch{Start: start, End: prev})
			start = ts
		}
		prev = ts
	}
	return append(out, stretch{Start: start, End: prev})
}

// sessionProjects joins session ids to project names using the cost log, which
// is the only source on disk carrying both. The event log stamps a session id
// on every transition and never a project.
func sessionProjects(cl *costLog) map[string]string {
	out := map[string]string{}
	keys := make([]sessionKey, 0, len(cl.Series))
	for k := range cl.Series {
		keys = append(keys, k)
	}
	// Sorted so a session that somehow appears under two projects resolves the
	// same way on every run: a back-fill whose output depends on map iteration
	// order cannot be checked against its own dry run.
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Session != keys[j].Session {
			return keys[i].Session < keys[j].Session
		}
		return keys[i].Project < keys[j].Project
	})
	for _, k := range keys {
		if _, seen := out[k.Session]; !seen {
			out[k.Session] = k.Project
		}
	}
	return out
}

// dropOverlappingLive removes reconstructed spans that reach into the era the
// daemon has already measured, and reports how many it dropped.
//
// liveFloor is the earliest MEASURED span on record (0 = nothing measured yet,
// which leaves the reconstruction unbounded on the right). A span ENDING at or
// after it is refused: the same run is very likely already in the log as a
// measured row, and two rows for one run inflate every figure in the section
// while looking exactly like a busier week.
//
// Dropped, not clipped, for the reason every other rule here drops: a clipped
// span has an end nobody observed.
func dropOverlappingLive(spans []outbound.AutonomySpan, liveFloor int64) ([]outbound.AutonomySpan, int) {
	if liveFloor <= 0 {
		return spans, 0
	}
	out := spans[:0:0]
	dropped := 0
	for _, s := range spans {
		if s.End >= liveFloor {
			dropped++
			continue
		}
		out = append(out, s)
	}
	return out, dropped
}

// sortSpans orders spans by start, then session, matching the order the span
// store reads them back in.
func sortSpans(spans []outbound.AutonomySpan) {
	sort.Slice(spans, func(i, j int) bool {
		if spans[i].Start != spans[j].Start {
			return spans[i].Start < spans[j].Start
		}
		return spans[i].Session < spans[j].Session
	})
}

// earliestTransition is the first instant the event log can speak for — the
// boundary the cost-log era stops at. 0 when the log holds no transition,
// which makes the cost era unbounded on the right.
func earliestTransition(log *eventLog) int64 {
	if len(log.Transitions) == 0 {
		return 0
	}
	return log.Transitions[0].TS
}
