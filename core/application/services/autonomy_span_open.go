package services

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// Open-run bookkeeping (#1905 recording).
//
// A span used to exist only in the daemon's memory until it closed, which made
// the daemon's own lifetime an invisible ceiling on what the feature could
// report. Two consequences, and they compound:
//
//   - a run in progress was worth nothing until it finished, so the longest run
//     on the machine was always the one the section could not show;
//   - a run whose daemon died was worth nothing EVER, and the longer a run, the
//     likelier it crossed a restart.
//
// The store's open-run journal is the fix, and this file is the daemon's half
// of it: journal a run the moment it opens, refresh it while it lasts, adopt it
// after a restart, and settle it if nobody ever comes back for it.

const (
	// autonomyRecoveryWindow is how long a run left in the journal by a
	// previous daemon waits to be adopted by the session it belongs to.
	//
	// Two minutes, which is a discovery budget rather than a guess: the
	// watchers' initial scan emits every live transcript in the first seconds,
	// but a session is only rediscovered once its adapter's watcher is
	// running, and monitoring is consent-gated (#570) — a permission exercised
	// during startup can put a watcher up appreciably after the event loop.
	//
	// Nothing is lost by the wait: an unadopted run is closed where the
	// previous daemon last SAW it, not at the deadline, so waiting longer
	// costs only how soon the row appears, never what it says.
	autonomyRecoveryWindow = 2 * time.Minute

	// autonomyOpenSyncInterval is how often the journal's last-seen instants
	// are pushed forward when nothing about the open set has changed.
	//
	// It bounds what a crash costs: an unadopted run is closed at its last-seen
	// instant, so a daemon killed mid-run under-reports that run by at most
	// this much. Thirty seconds against a feature whose subject is multi-hour
	// runs — and one small write per half minute instead of one every tick.
	autonomyOpenSyncInterval = 30 * time.Second
)

// loadRecoverableAutonomySpans reads the open-run journal a previous daemon
// left behind and arms the adoption window.
//
// Nothing is closed here. A run in the journal is a run that WAS going when the
// last daemon stopped looking, and whether it is still going is a question only
// rediscovery can answer — so every entry waits, and settleUnadoptedAutonomySpans
// deals with the ones nobody claims.
func (d *SessionDetector) loadRecoverableAutonomySpans() {
	if d.autonomySpans == nil {
		return
	}
	open, err := d.autonomySpans.OpenSpans()
	if err != nil {
		d.log.LogError(logComponentAutonomySpans, "", err.Error())
		return
	}
	deadline := d.nowFn().Add(autonomyRecoveryWindow).Unix()
	d.mu.Lock()
	for _, s := range open {
		if s.Session == "" {
			continue
		}
		d.autonomyRecovered[s.Session] = s
	}
	d.autonomyRecoveryDeadline = deadline
	n := len(d.autonomyRecovered)
	d.mu.Unlock()
	if n > 0 {
		d.log.LogInfo(logComponentAutonomySpans, "",
			strconv.Itoa(n)+" autonomous run(s) were still open when the last daemon stopped; "+
				"each is held for adoption by its session and otherwise closed where it was last seen")
	}
}

// adoptRecoveredAutonomySpan hands back the recovered run for a session being
// rediscovered, removing it from the pending set so it can never be settled
// afterwards as well.
//
// Past the deadline it adopts NOTHING, and that is the whole guard against
// resurrection: a session id that reappears an hour after the daemon started is
// not the run that was open when the daemon before it died — settleUnadopted
// has already closed that one where it was last seen, and adopting it now would
// splice the downtime, plus everything since, into a single fabricated run.
func (d *SessionDetector) adoptRecoveredAutonomySpan(sessionID string) (outbound.AutonomySpan, bool) {
	if sessionID == "" {
		return outbound.AutonomySpan{}, false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.autonomyRecoveryDeadline == 0 || d.nowFn().Unix() > d.autonomyRecoveryDeadline {
		return outbound.AutonomySpan{}, false
	}
	span, ok := d.autonomyRecovered[sessionID]
	if !ok {
		return outbound.AutonomySpan{}, false
	}
	delete(d.autonomyRecovered, sessionID)
	return span, true
}

// settleUnadoptedAutonomySpans closes every recovered run nobody claimed, once
// the adoption window has passed. Idempotent: the pending set is emptied and
// the deadline disarmed, so it does its work exactly once per daemon.
//
// Each is closed AT ITS LAST-SEEN INSTANT, with an end reason of unknown. Both
// halves are the honest choice and both matter:
//
//   - at last-seen, not now, because the daemon was not watching in between and
//     crediting a run with the downtime is how a five-minute run becomes an
//     overnight one;
//   - unknown, because nothing observed why it stopped. `ready` would claim it
//     finished its turn, which is the one thing a run interrupted by a dying
//     daemon has no evidence of.
func (d *SessionDetector) settleUnadoptedAutonomySpans(now int64) {
	d.mu.Lock()
	if d.autonomyRecoveryDeadline == 0 || now <= d.autonomyRecoveryDeadline {
		d.mu.Unlock()
		return
	}
	pending := make([]outbound.AutonomySpan, 0, len(d.autonomyRecovered))
	for _, s := range d.autonomyRecovered {
		pending = append(pending, s)
	}
	d.autonomyRecovered = make(map[string]outbound.AutonomySpan)
	d.autonomyRecoveryDeadline = 0
	d.mu.Unlock()

	sort.Slice(pending, func(i, j int) bool { return pending[i].Session < pending[j].Session })
	for _, s := range pending {
		s.Running = false
		s.Reason = session.AutonomyReasonUnknown
		if d.autonomySpans == nil {
			continue
		}
		if err := d.autonomySpans.RecordSpan(s); err != nil {
			d.log.LogError(logComponentAutonomySpans, s.Session, err.Error())
		}
	}
}

// journalOpenAutonomySpan records the session's open run the instant it opens,
// so a daemon that dies seconds later still leaves it recoverable. The
// reconciling SyncOpenSpans on the refresh ticker is what later moves its
// last-seen instant forward and removes it if the session disappears.
func (d *SessionDetector) journalOpenAutonomySpan(state *session.SessionState, now int64) {
	if d.autonomySpans == nil || state.AutonomySpanStart == nil {
		return
	}
	if err := d.autonomySpans.RecordOpenSpan(openAutonomySpanOf(state, now)); err != nil {
		d.log.LogError(logComponentAutonomySpans, state.SessionID, err.Error())
	}
}

// syncOpenAutonomySpans reconciles the journal against the sessions that
// actually hold an open run right now, and reports whether it wrote.
//
// Throttled on two conditions, not one: the set CHANGING is what has to be
// written immediately (a run that just ended must stop being reported as
// running), while an unchanged set only needs its last-seen instants pushed
// forward, which is worth one write per autonomyOpenSyncInterval and no more.
//
// A session whose run is held by the flicker grace still counts as open — the
// grace has not decided yet, and a daemon that dies mid-grace should recover a
// run rather than lose one.
func (d *SessionDetector) syncOpenAutonomySpans(states []*session.SessionState, now int64) bool {
	if d.autonomySpans == nil {
		return false
	}
	open := make([]outbound.AutonomySpan, 0, 4)
	for _, state := range states {
		if state == nil || state.AutonomySpanStart == nil {
			continue
		}
		open = append(open, openAutonomySpanOf(state, now))
	}
	key := autonomyOpenSetKey(open)

	d.mu.Lock()
	unchanged := key == d.autonomyOpenSyncKey
	fresh := now-d.autonomyOpenSyncAt < int64(autonomyOpenSyncInterval/time.Second)
	if unchanged && fresh {
		d.mu.Unlock()
		return false
	}
	d.autonomyOpenSyncKey = key
	d.autonomyOpenSyncAt = now
	d.mu.Unlock()

	if err := d.autonomySpans.SyncOpenSpans(open); err != nil {
		d.log.LogError(logComponentAutonomySpans, "", err.Error())
		return false
	}
	return true
}

// openAutonomySpanOf renders a session's open run as a span whose End is the
// instant it was last seen alive — never an end, which is why Running is set
// and no reason is stamped.
func openAutonomySpanOf(state *session.SessionState, now int64) outbound.AutonomySpan {
	return outbound.AutonomySpan{
		Start:           *state.AutonomySpanStart,
		End:             now,
		Project:         state.ProjectName,
		Session:         state.SessionID,
		Adapter:         state.Adapter,
		Model:           autonomySpanModel(state),
		Kind:            session.AutonomyKindForParent(state.ParentSessionID),
		Parent:          state.ParentSessionID,
		Running:         true,
		StartLowerBound: state.AutonomySpanStartLowerBound,
	}
}

// autonomyOpenSetKey is a signature of WHICH runs are open and when each began
// — deliberately not of their last-seen instants, which change every tick and
// would defeat the throttle they are being compared for.
func autonomyOpenSetKey(open []outbound.AutonomySpan) string {
	ids := make([]string, 0, len(open))
	for _, s := range open {
		ids = append(ids, s.Session+"@"+strconv.FormatInt(s.Start, 10))
	}
	sort.Strings(ids)
	return strings.Join(ids, ",")
}
