package notify

import (
	"sort"
	"time"
)

// payloadVersion is stamped into every Payload so installed service workers
// can refuse shapes newer than they understand instead of mis-rendering them.
const payloadVersion = 1

// maxSummarySessions caps the member labels a summary payload carries: the
// banner names a handful and says "N agents need attention", and the
// envelope is padded to a fixed size anyway (arc42 §8.2), so labels past six
// buy nothing.
const maxSummarySessions = 6

// summaryTopic collapses every burst summary onto one notification: a newer
// "N agents need attention" replaces the last (§8.4).
const summaryTopic = "summary"

func daemonTopic(id string) string { return "daemon:" + id }

// session is the engine's record of one live session.
type session struct {
	state State
	// stateAt is when state last *changed*; same-state updates leave it
	// alone, so for a ready record it doubles as the start of the current
	// continuous-ready run. Only rekey merges read it.
	stateAt time.Time
	parent  string
	label   string
	project string

	// lastWaitingPush / lastReadyPush / lastErrorPush are the
	// per-(session, edge) cooldown stamps (§8.4). Zero means never pushed on
	// that edge. Reached only through cooldownStamp.
	lastWaitingPush time.Time
	lastReadyPush   time.Time
	lastErrorPush   time.Time

	holdDownPending bool
	holdDownAt      time.Time
}

// cooldownStamp addresses one edge's cooldown stamp, for reading and for
// writing. A pointer rather than a get/set pair on purpose: two parallel
// switches would have to agree on which field each edge maps to, and a
// disagreement between them is silent — the read consults one stamp while
// the write refreshes another, so the edge never comes off cooldown, or
// never goes on. There is one mapping here because there is one switch.
//
// An unrecognized edge shares the waiting stamp, which is where the engine's
// degrade-silent path already sends anything it does not know.
func (s *session) cooldownStamp(edge State) *time.Time {
	switch edge {
	case StateReady:
		return &s.lastReadyPush
	case StateError:
		return &s.lastErrorPush
	default:
		return &s.lastWaitingPush
	}
}

func (s *session) cancelHoldDown() {
	s.holdDownPending = false
	s.holdDownAt = time.Time{}
}

// daemon is the engine's record of one daemon link (§6.4).
type daemon struct {
	label string
	down  bool
	// gracePending/graceAt: armed by the first down while up; a reconnect
	// within grace cancels silently, because roaming reconnects are seconds
	// (§6.4).
	gracePending bool
	graceAt      time.Time
	// downPushOutstanding marks that a disconnect banner is on the phone,
	// so the next up must replace it silently.
	downPushOutstanding bool
}

// windowEntry is one candidate that survived cooldown, timestamped for the
// sliding burst window.
type windowEntry struct {
	at        time.Time
	sessionID string
}

// Engine folds transition events into delivery decisions. The zero value is
// unusable; construct with New. Not safe for concurrent use — see the
// package comment.
type Engine struct {
	cfg      Config
	sessions map[string]*session
	daemons  map[string]*daemon

	// window holds every candidate that survived cooldown within the last
	// BurstWindow — including candidates absorbed into summaries. Entries
	// outlive deletes of their session: they are history, and a delete
	// cannot unmake the load they represent. A rekey renames its entries in
	// place, though — the window tracks logical sessions, and a renamed
	// session is not a second agent in the summary's count.
	window []windowEntry
	// summaryRenotifyArmed: the first summary of a burst episode buzzes
	// (Renotify true); refreshes replace silently until a candidate finds
	// the window back under threshold, which re-arms the next episode.
	summaryRenotifyArmed bool
}

// New builds an Engine, applying DefaultConfig() values for zero fields
// (lets tests shorten individual knobs without restating the rest).
func New(cfg Config) *Engine {
	def := DefaultConfig()
	if cfg.HoldDown == 0 {
		cfg.HoldDown = def.HoldDown
	}
	if cfg.Cooldown == 0 {
		cfg.Cooldown = def.Cooldown
	}
	if cfg.BurstWindow == 0 {
		cfg.BurstWindow = def.BurstWindow
	}
	if cfg.BurstThreshold == 0 {
		cfg.BurstThreshold = def.BurstThreshold
	}
	if cfg.TTLWaiting == 0 {
		cfg.TTLWaiting = def.TTLWaiting
	}
	if cfg.TTLReady == 0 {
		cfg.TTLReady = def.TTLReady
	}
	if cfg.TTLError == 0 {
		cfg.TTLError = def.TTLError
	}
	if cfg.TTLDaemon == 0 {
		cfg.TTLDaemon = def.TTLDaemon
	}
	if cfg.DaemonGrace == 0 {
		cfg.DaemonGrace = def.DaemonGrace
	}
	return &Engine{
		cfg:                  cfg,
		sessions:             make(map[string]*session),
		daemons:              make(map[string]*daemon),
		summaryRenotifyArmed: true,
	}
}

// Handle folds one event into the policy state and returns the pushes it
// decides. Due-timer pushes come first: both Handle and Tick fire any
// elapsed hold-downs and daemon graces before anything else, so chronology
// holds even when the caller is late. now must be non-decreasing across
// calls.
func (e *Engine) Handle(ev Event, now time.Time) []Push {
	pushes := e.fireDue(now)
	switch ev.Kind {
	case EventSessionUpdate:
		pushes = append(pushes, e.sessionUpdate(ev, now)...)
	case EventSessionDelete:
		e.sessionDelete(ev)
	case EventRekey:
		e.rekey(ev)
	case EventDaemonDown:
		e.daemonDown(ev, now)
	case EventDaemonUp:
		pushes = append(pushes, e.daemonUp(ev, now)...)
	default:
		// Unknown kinds are wire input from a newer peer: ignored, never a
		// panic — domain code never panics on input.
	}
	return pushes
}

// Tick fires any timers due at now — ready hold-downs and daemon disconnect
// graces — and returns their pushes. The caller schedules Ticks off
// NextWake; a late Tick still fires with the now it is given.
func (e *Engine) Tick(now time.Time) []Push {
	return e.fireDue(now)
}

// NextWake returns the earliest pending timer (hold-down or daemon grace);
// ok=false when nothing is pending. The caller schedules its next Tick off
// it.
func (e *Engine) NextWake() (time.Time, bool) {
	var wake time.Time
	var ok bool
	consider := func(t time.Time) {
		if !ok || t.Before(wake) {
			wake, ok = t, true
		}
	}
	for _, rec := range e.sessions {
		if rec.holdDownPending {
			consider(rec.holdDownAt)
		}
	}
	for _, d := range e.daemons {
		if d.gracePending {
			consider(d.graceAt)
		}
	}
	return wake, ok
}

// fireDue runs every timer that has elapsed by now. Hold-downs fire in
// sorted session-id order, then graces in sorted daemon-id order — wherever
// several pushes emerge from one call, the order is deterministic.
func (e *Engine) fireDue(now time.Time) []Push {
	var pushes []Push
	var dueSessions []string
	for id, rec := range e.sessions {
		if rec.holdDownPending && !rec.holdDownAt.After(now) {
			dueSessions = append(dueSessions, id)
		}
	}
	sort.Strings(dueSessions)
	for _, id := range dueSessions {
		rec := e.sessions[id]
		rec.cancelHoldDown()
		// State is necessarily still ready here: every path that leaves
		// ready — re-working (§6.2), waiting, a subagent event, an unknown
		// state name, a delete, a rekey merge landing elsewhere — cancels
		// the hold-down before this can run.
		pushes = append(pushes, e.candidate(id, rec, StateReady, now)...)
	}

	var dueDaemons []string
	for id, d := range e.daemons {
		if d.gracePending && !d.graceAt.After(now) {
			dueDaemons = append(dueDaemons, id)
		}
	}
	sort.Strings(dueDaemons)
	for _, id := range dueDaemons {
		d := e.daemons[id]
		d.gracePending = false
		if !d.down {
			continue // an up cancels the grace, so this is unreachable; kept as the §6.4 "still down" guard
		}
		d.downPushOutstanding = true
		pushes = append(pushes, Push{
			Topic:    daemonTopic(id),
			TTL:      e.cfg.TTLDaemon,
			Urgency:  UrgencyNormal,
			Renotify: true,
			Payload: Payload{
				Version:     payloadVersion,
				Kind:        PushDaemonDown,
				DaemonID:    id,
				DaemonLabel: d.label,
				At:          now.Unix(),
			},
		})
	}
	return pushes
}

// sessionUpdate applies one session_update. First sightings record silently
// — live or snapshot-seeded (§8.4) — which is what makes snapshot reconcile
// need no special casing: an unchanged session diffs to nothing and a
// genuine diff notifies (§6.3).
func (e *Engine) sessionUpdate(ev Event, now time.Time) []Push {
	if ev.SessionID == "" {
		return nil
	}
	rec, known := e.sessions[ev.SessionID]
	if !known {
		e.sessions[ev.SessionID] = &session{
			state:   ev.State,
			stateAt: now,
			parent:  ev.ParentID,
			label:   ev.Label,
			project: ev.Project,
		}
		return nil
	}
	prev := rec.state
	rec.parent, rec.label, rec.project = ev.ParentID, ev.Label, ev.Project
	if rec.parent != "" {
		// A parentage flip cannot arrive on a conforming wire (events.md
		// fixes it at creation), but wire input is untrusted, and a session
		// recorded as a subagent must never notify (§8.4) — so even a
		// same-state update that flips ParentID kills a pending hold-down,
		// or the fire path below would push for a subagent.
		rec.cancelHoldDown()
	}
	if ev.State == prev {
		// The daemon emits session_updated on every metrics tick —
		// same-state updates are the common case and must change nothing
		// beyond the descriptive fields above.
		return nil
	}
	rec.state = ev.State
	rec.stateAt = now

	switch {
	case ev.ParentID != "":
		// Subagents never notify — the parent covers them (§8.4, matching
		// both existing client implementations).
		rec.cancelHoldDown()
		return nil
	case ev.State == StateWorking:
		// §6.2: re-entering working within the hold-down means the ready
		// was a flap — cancel. Entering working never pushes.
		rec.cancelHoldDown()
		return nil
	case ev.State == StateWaiting:
		rec.cancelHoldDown()
		return e.candidate(ev.SessionID, rec, StateWaiting, now)
	case ev.State == StateError:
		// Not held down, unlike ready. §6.2's hold-down exists because a
		// ready is undone by a follow-up prompt within seconds; an error is
		// cleared by the next SUCCESSFUL turn and by nothing else
		// (session.StateError), so there is no flap to wait out. A session
		// whose agent died mid-turn is the case a phone is most useful for,
		// and holding it for 7s would only delay it.
		rec.cancelHoldDown()
		return e.candidate(ev.SessionID, rec, StateError, now)
	case ev.State == StateReady:
		// events.md says only working → ready occurs, but wire input is
		// untrusted: any prev arms (or replaces) the hold-down.
		rec.holdDownPending = true
		rec.holdDownAt = now.Add(e.cfg.HoldDown)
		return nil
	default:
		// A future state name degrades silent rather than crashing or
		// spamming. It still left ready, so the pending hold-down goes —
		// the fire path relies on every leaving path cancelling.
		rec.cancelHoldDown()
		return nil
	}
}

// candidate runs one push candidate through cooldown and burst coalescing
// (§8.4). edge is StateWaiting, StateReady or StateError, and equals
// rec.state on every path that reaches here.
func (e *Engine) candidate(id string, rec *session, edge State, now time.Time) []Push {
	last := rec.cooldownStamp(edge)
	// A suppressed candidate does not refresh the stamp — otherwise a
	// flapping session could silence itself forever. Exactly Cooldown
	// elapsed is allowed.
	if !last.IsZero() && now.Sub(*last) < e.cfg.Cooldown {
		return nil
	}
	*last = now

	e.window = append(e.window, windowEntry{at: now, sessionID: id})
	e.pruneWindow(now)
	if len(e.window) > e.cfg.BurstThreshold {
		return []Push{e.summaryPush(now)}
	}
	// Back under threshold: this candidate pushes individually and the
	// current burst episode (if any) is over — the next summary buzzes
	// again.
	e.summaryRenotifyArmed = true

	// A stale ready is noise, so it expires in ten minutes and defers to the
	// device's power state. A waiting and an error both stay TRUE until a
	// human or a successful turn changes them, so both keep the long TTL and
	// the urgency that wakes the phone.
	ttl, urgency := e.cfg.TTLWaiting, UrgencyHigh
	switch edge {
	case StateReady:
		ttl, urgency = e.cfg.TTLReady, UrgencyNormal
	case StateError:
		ttl = e.cfg.TTLError
	}
	return []Push{{
		Topic:    id,
		TTL:      ttl,
		Urgency:  urgency,
		Renotify: true,
		Payload: Payload{
			Version:   payloadVersion,
			Kind:      PushSession,
			SessionID: id,
			Label:     rec.label,
			Project:   rec.project,
			State:     edge,
			At:        now.Unix(),
		},
	}}
}

// pruneWindow drops burst-window entries that have aged out. An entry at
// exactly BurstWindow old is outside the window.
func (e *Engine) pruneWindow(now time.Time) {
	cut := now.Add(-e.cfg.BurstWindow)
	keep := e.window[:0]
	for _, w := range e.window {
		if w.at.After(cut) {
			keep = append(keep, w)
		}
	}
	e.window = keep
}

// summaryPush composes the "N agents need attention" coalescer: distinct
// sessions in the window, their labels (falling back to the session id)
// sorted and capped, on the single summary topic.
func (e *Engine) summaryPush(now time.Time) Push {
	distinct := make(map[string]bool, len(e.window))
	for _, w := range e.window {
		distinct[w.sessionID] = true
	}
	labels := make([]string, 0, len(distinct))
	for id := range distinct {
		label := id
		// A member deleted or rekeyed away since its candidate has no
		// record anymore; its id still names it.
		if rec, ok := e.sessions[id]; ok && rec.label != "" {
			label = rec.label
		}
		labels = append(labels, label)
	}
	sort.Strings(labels)
	if len(labels) > maxSummarySessions {
		labels = labels[:maxSummarySessions]
	}
	renotify := e.summaryRenotifyArmed
	e.summaryRenotifyArmed = false
	return Push{
		Topic:    summaryTopic,
		TTL:      e.cfg.TTLWaiting,
		Urgency:  UrgencyHigh,
		Renotify: renotify,
		Payload: Payload{
			Version:  payloadVersion,
			Kind:     PushSummary,
			Count:    len(distinct),
			Sessions: labels,
			At:       now.Unix(),
		},
	}
}

// sessionDelete drops the session record entirely: the pending hold-down is
// cancelled and the cooldown stamps are forgotten (§8.4 "no push; cancels
// pending hold-downs"). Burst-window entries already recorded stay — they
// are history.
func (e *Engine) sessionDelete(ev Event) {
	delete(e.sessions, ev.SessionID)
}

// rekey moves OldSessionID's policy state — cooldown stamps, pending
// hold-down, last-known state — under SessionID, so a presession promoted
// to a real session cannot restart its cooldowns (§8.4, the #1002 class).
// An unknown old id is a no-op: there is nothing to move, and inventing a
// record would fake a first sighting that never happened.
func (e *Engine) rekey(ev Event) {
	if ev.OldSessionID == "" || ev.SessionID == "" || ev.OldSessionID == ev.SessionID {
		return
	}
	// Rename burst-window history first — even when the old record is gone
	// (deleted since its candidate), the entries describe the same logical
	// session, and leaving them under the old id double-counts it in the
	// summary's user-visible "N agents need attention".
	for i := range e.window {
		if e.window[i].sessionID == ev.OldSessionID {
			e.window[i].sessionID = ev.SessionID
		}
	}
	old, ok := e.sessions[ev.OldSessionID]
	if !ok {
		return
	}
	delete(e.sessions, ev.OldSessionID)
	dst, exists := e.sessions[ev.SessionID]
	if !exists {
		e.sessions[ev.SessionID] = old
		return
	}
	// Both ids were observed (the promotion raced real updates): merge. The
	// newer state wins; a tie keeps the record already under the real id,
	// which is where live updates land — and its label/project/parent stay
	// for the same reason. When both strands agree on the state, the
	// continuous run began at the earlier change: the surviving hold-down
	// may be the earlier strand's, and the run start must not postdate the
	// arm that produced it.
	switch {
	case old.state == dst.state:
		if old.stateAt.Before(dst.stateAt) {
			dst.stateAt = old.stateAt
		}
	case old.stateAt.After(dst.stateAt):
		dst.state, dst.stateAt = old.state, old.stateAt
	}
	if old.lastWaitingPush.After(dst.lastWaitingPush) {
		dst.lastWaitingPush = old.lastWaitingPush
	}
	if old.lastReadyPush.After(dst.lastReadyPush) {
		dst.lastReadyPush = old.lastReadyPush
	}
	if old.lastErrorPush.After(dst.lastErrorPush) {
		dst.lastErrorPush = old.lastErrorPush
	}
	if old.holdDownPending && (!dst.holdDownPending || old.holdDownAt.Before(dst.holdDownAt)) {
		dst.holdDownPending, dst.holdDownAt = true, old.holdDownAt
	}
	if dst.state != StateReady || dst.parent != "" {
		// A merge landing on a non-ready state has left ready like any
		// other path, and the fire path relies on every such path
		// cancelling. A merge landing on a subagent kills the hold-down
		// too: subagents never notify (§8.4), so one cannot inherit a
		// pending push.
		dst.cancelHoldDown()
	}
}

// daemonDown marks a daemon link down and arms the disconnect grace (§6.4).
// No immediate push: roaming reconnects are seconds, and the grace is the
// watchdog's debounce.
func (e *Engine) daemonDown(ev Event, now time.Time) {
	if ev.DaemonID == "" {
		return
	}
	d, ok := e.daemons[ev.DaemonID]
	if !ok {
		d = &daemon{}
		e.daemons[ev.DaemonID] = d
	}
	d.label = ev.DaemonLabel
	if d.down {
		// Repeated down frames while down must not push the grace out — a
		// flapping link could otherwise postpone the watchdog forever.
		return
	}
	d.down = true
	d.gracePending = true
	d.graceAt = now.Add(e.cfg.DaemonGrace)
}

// daemonUp clears a daemon's down state. Within grace it cancels silently;
// after a disconnect banner went out it replaces that banner without a
// second buzz — Renotify=false, §6.4 "Topic-replaced by later status".
func (e *Engine) daemonUp(ev Event, now time.Time) []Push {
	if ev.DaemonID == "" {
		return nil
	}
	d, ok := e.daemons[ev.DaemonID]
	if !ok {
		// First sighting of a daemon: record silently, like a session's.
		e.daemons[ev.DaemonID] = &daemon{label: ev.DaemonLabel}
		return nil
	}
	d.label = ev.DaemonLabel
	d.down = false
	if d.gracePending {
		d.gracePending = false
		return nil
	}
	if !d.downPushOutstanding {
		return nil
	}
	d.downPushOutstanding = false
	return []Push{{
		Topic:    daemonTopic(ev.DaemonID),
		TTL:      e.cfg.TTLDaemon,
		Urgency:  UrgencyNormal,
		Renotify: false,
		Payload: Payload{
			Version:     payloadVersion,
			Kind:        PushDaemonUp,
			DaemonID:    ev.DaemonID,
			DaemonLabel: d.label,
			At:          now.Unix(),
		},
	}}
}
