package notify

import (
	"fmt"
	"math/rand"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
)

// TestStormInvariants drives random event storms through the engine and
// grades the emitted pushes against an independent oracle (arc42 §8.7). The
// seed is fixed so a failure reproduces; the failure message prints the
// config and the full event/tick sequence.
//
// Generator axes (each varied independently, so a blind spot on one axis is
// not shared by the others — the hookjson splicer's widened-generator
// lesson):
//   - session count: 1–6 base ids, growing as rekeys mint fresh ids;
//   - subagent ratio: ~1 in 4 base ids carries a fixed ParentID (parents
//     never flip mid-stream — on the real wire parentage is fixed at
//     creation by the transcript path, see events.md);
//   - event-kind mix: updates dominate, plus deletes, rekeys (to fresh ids
//     AND onto existing ids, forcing merges), daemon flaps on two ids, and
//     malformed frames (empty ids, unknown kinds — wire input is untrusted);
//   - state jumps: legal and illegal alike, drawn uniformly, plus an
//     unknown state name and explicit duplicate same-state updates;
//   - time steps: zero, small, clustered one millisecond around each of the
//     HoldDown / BurstWindow / Cooldown / DaemonGrace boundaries, and far
//     beyond them;
//   - flap bursts: several distinct sessions driven working→waiting within
//     milliseconds (the R3 "ten flapping sessions" scenario) — without this
//     axis the burst threshold is crossed almost never and the summary arm
//     of the oracle grades nothing;
//   - interleaved Ticks at irregular times, under two configs (the §8.4
//     defaults and a shrunk one that packs many timer firings per storm).
func TestStormInvariants(t *testing.T) {
	runStorms(t, 20260814, 2000)
}

// TestStormSweep is the multi-seed local sweep run before changing the
// engine; it is opt-in so the committed suite stays fast.
func TestStormSweep(t *testing.T) {
	if os.Getenv("NOTIFY_STORM_SWEEP") == "" {
		t.Skip("set NOTIFY_STORM_SWEEP=1 for the larger multi-seed sweep")
	}
	for _, seed := range []int64{1, 2, 3} {
		runStorms(t, seed, 8000)
	}
}

func runStorms(t *testing.T, seed int64, iterations int) {
	rng := rand.New(rand.NewSource(seed))
	for i := 0; i < iterations; i++ {
		runStorm(t, rng, seed, i)
	}
}

// fastConfig shrinks every timing knob so a few dozen steps cross many
// boundaries; TTLs are distinct oddball values so a swapped constant cannot
// hide behind an equal one.
func fastConfig() Config {
	return Config{
		HoldDown:       50 * time.Millisecond,
		Cooldown:       800 * time.Millisecond,
		BurstWindow:    300 * time.Millisecond,
		BurstThreshold: 2,
		TTLWaiting:     111 * time.Second,
		TTLReady:       222 * time.Second,
		TTLError:       444 * time.Second,
		TTLDaemon:      333 * time.Second,
		DaemonGrace:    400 * time.Millisecond,
	}
}

func randDelta(rng *rand.Rand, cfg Config) time.Duration {
	const eps = time.Millisecond
	anchors := []time.Duration{
		0,
		time.Duration(rng.Intn(50)) * time.Millisecond,
		cfg.HoldDown - eps, cfg.HoldDown, cfg.HoldDown + eps,
		cfg.BurstWindow - eps, cfg.BurstWindow, cfg.BurstWindow + eps,
		cfg.Cooldown - eps, cfg.Cooldown, cfg.Cooldown + eps,
		cfg.DaemonGrace - eps, cfg.DaemonGrace, cfg.DaemonGrace + eps,
		5 * cfg.Cooldown,
	}
	return anchors[rng.Intn(len(anchors))]
}

func runStorm(t *testing.T, rng *rand.Rand, seed int64, iter int) {
	t.Helper()
	cfg := DefaultConfig()
	if rng.Intn(2) == 0 {
		cfg = fastConfig()
	}
	eng := New(cfg)
	o := newStormOracle(cfg)

	base := 1 + rng.Intn(6)
	universe := make([]string, 0, base+8)
	parentOf := map[string]string{}
	for i := 0; i < base; i++ {
		id := fmt.Sprintf("s%d", i)
		universe = append(universe, id)
		if rng.Intn(4) == 0 {
			parentOf[id] = "parent-of-" + id
		}
	}
	daemons := []string{"d0", "d1"}
	rekeyed := 0

	start := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	now := start
	var log []string
	fail := func(format string, args ...any) {
		t.Fatalf("storm seed=%d iteration=%d: %s\ncfg: %+v\nsequence:\n%s",
			seed, iter, fmt.Sprintf(format, args...), cfg, strings.Join(log, "\n"))
	}
	afterCall := func(pushes []Push) {
		if len(pushes) > 0 {
			log = append(log, fmt.Sprintf("   -> %d push(es): %+v", len(pushes), pushes))
		}
		o.checkPushes(fail, pushes, now)
		// After firing due timers at now, every remaining timer is strictly
		// in the future — a NextWake at or before now means fireDue missed
		// one.
		if wake, ok := eng.NextWake(); ok && !wake.After(now) {
			fail("NextWake %v not after now %v", wake, now)
		}
	}
	handle := func(desc string, ev Event) {
		log = append(log, fmt.Sprintf("t=%-12s %s", now.Sub(start), desc))
		pushes := eng.Handle(ev, now)
		afterCall(pushes)
		o.applyEvent(ev, now)
	}

	steps := 20 + rng.Intn(41)
	for s := 0; s < steps; s++ {
		now = now.Add(randDelta(rng, cfg))
		switch roll := rng.Intn(100); {
		case roll < 55: // session update
			id := universe[rng.Intn(len(universe))]
			var st State
			if rec, known := o.sessions[id]; known && rng.Intn(4) == 0 {
				st = rec.state // explicit duplicate same-state update
			} else {
				jumps := append(allStates, State("compacting"))
				st = jumps[rng.Intn(len(jumps))]
			}
			label := "L-" + id
			if rng.Intn(3) == 0 {
				label = ""
			}
			ev := Event{Kind: EventSessionUpdate, SessionID: id, ParentID: parentOf[id], State: st, Label: label, Project: "proj"}
			handle(fmt.Sprintf("update %s state=%s parent=%q label=%q", id, st, ev.ParentID, label), ev)
		case roll < 63: // delete
			id := universe[rng.Intn(len(universe))]
			handle("delete "+id, Event{Kind: EventSessionDelete, SessionID: id})
		case roll < 70: // rekey
			// Old ids are drawn from the non-subagent pool: parentage never
			// flips on the real wire, and letting a moved record change
			// parent under an id's fixed ParentID would make I1's "at push
			// time" ambiguous inside a single call.
			old := universe[rng.Intn(len(universe))]
			if parentOf[old] != "" {
				continue
			}
			var target string
			if rng.Intn(5) < 3 {
				target = fmt.Sprintf("r%d", rekeyed)
				rekeyed++
				universe = append(universe, target)
			} else {
				target = universe[rng.Intn(len(universe))]
			}
			handle(fmt.Sprintf("rekey %s -> %s", old, target), Event{Kind: EventRekey, OldSessionID: old, SessionID: target})
		case roll < 78: // flap burst: distinct sessions hit waiting almost at once
			for i := 0; i < 3+rng.Intn(4) && i < len(universe); i++ {
				id := universe[(s+i)%len(universe)]
				now = now.Add(time.Duration(rng.Intn(3)) * time.Millisecond)
				ev := Event{Kind: EventSessionUpdate, SessionID: id, ParentID: parentOf[id], State: StateWorking, Label: "L-" + id}
				handle(fmt.Sprintf("burst update %s state=working parent=%q", id, ev.ParentID), ev)
				ev.State = StateWaiting
				handle(fmt.Sprintf("burst update %s state=waiting parent=%q", id, ev.ParentID), ev)
			}
		case roll < 86: // daemon flap
			id := daemons[rng.Intn(len(daemons))]
			if rng.Intn(2) == 0 {
				handle("daemon down "+id, Event{Kind: EventDaemonDown, DaemonID: id, DaemonLabel: "mac-" + id})
			} else {
				handle("daemon up "+id, Event{Kind: EventDaemonUp, DaemonID: id, DaemonLabel: "mac-" + id})
			}
		case roll < 90: // malformed frames
			garbage := []Event{
				{Kind: EventKind("bogus"), SessionID: "s0"},
				{Kind: EventSessionUpdate, State: StateWaiting},
				{Kind: EventRekey, SessionID: "x"},
				{Kind: EventRekey, OldSessionID: "x"},
				{Kind: EventDaemonDown},
				{Kind: EventDaemonUp},
			}
			ev := garbage[rng.Intn(len(garbage))]
			handle(fmt.Sprintf("garbage kind=%s", ev.Kind), ev)
		default: // tick
			log = append(log, fmt.Sprintf("t=%-12s tick", now.Sub(start)))
			afterCall(eng.Tick(now))
		}
	}
}

// stormOracle is an independent trace grader: it keeps just enough
// bookkeeping to judge every emitted push, and never re-implements the
// decision pipeline — a push the engine wrongly suppresses is the table
// tests' business, a push it wrongly emits is caught here.
type stormOracle struct {
	cfg      Config
	sessions map[string]*oracleSession
	everSeen map[string]bool
	daemons  map[string]*oracleDaemon
	// pushTimes holds session-kind push times for the sliding-window bound.
	pushTimes []time.Time
}

// oracleIdentity survives rekeys: cooldown invariants bind the union of ids
// a session has carried, not the current spelling.
type oracleIdentity struct {
	lastWaitingPush time.Time
	lastReadyPush   time.Time
	lastErrorPush   time.Time
}

type oracleSession struct {
	ident   *oracleIdentity
	state   State
	stateAt time.Time
	parent  string
}

type oracleDaemon struct {
	downSince   time.Time // zero = up
	bannerShown bool
}

func newStormOracle(cfg Config) *stormOracle {
	return &stormOracle{
		cfg:      cfg,
		sessions: map[string]*oracleSession{},
		everSeen: map[string]bool{},
		daemons:  map[string]*oracleDaemon{},
	}
}

// checkPushes grades one call's pushes against the model as it stood BEFORE
// the call's event is applied. That is sound on both push sources: due-timer
// pushes chronologically precede the event, and the event-driven session
// pushes (the waiting and error edges) require a session already known
// before the event.
//
// It is also why neither of those two edges is graded against rec.state the
// way the ready edge is: ready is fired by a timer, so the model already
// holds StateReady when its push is checked, while waiting and error are
// fired by the very event the model has not applied yet. That the error edge
// is not held down is asserted directly, in TestErrorIsNotHeldDown.
func (o *stormOracle) checkPushes(fail func(string, ...any), pushes []Push, now time.Time) {
	for i, p := range pushes {
		if p.Payload.Version != payloadVersion {
			fail("push %d: payload version %d", i, p.Payload.Version)
		}
		if p.Payload.At != now.Unix() {
			fail("push %d: At %d, want %d", i, p.Payload.At, now.Unix())
		}
		switch p.Payload.Kind {
		case PushSession:
			o.checkSessionPush(fail, i, p, now)
		case PushSummary:
			o.checkSummaryPush(fail, i, p)
		case PushDaemonDown:
			d := o.daemons[p.Payload.DaemonID]
			if d == nil || d.downSince.IsZero() {
				fail("push %d: I7: daemon_down for %q which is not down", i, p.Payload.DaemonID)
			}
			if now.Sub(d.downSince) < o.cfg.DaemonGrace {
				fail("push %d: I7: daemon_down after %v, grace is %v", i, now.Sub(d.downSince), o.cfg.DaemonGrace)
			}
			if p.TTL != o.cfg.TTLDaemon || p.Urgency != UrgencyNormal || !p.Renotify || p.Topic != "daemon:"+p.Payload.DaemonID {
				fail("push %d: I8: daemon_down headers wrong: %+v", i, p)
			}
			d.bannerShown = true
		case PushDaemonUp:
			d := o.daemons[p.Payload.DaemonID]
			if d == nil || !d.bannerShown {
				fail("push %d: daemon_up with no outstanding banner for %q", i, p.Payload.DaemonID)
			}
			if p.TTL != o.cfg.TTLDaemon || p.Urgency != UrgencyNormal || p.Renotify || p.Topic != "daemon:"+p.Payload.DaemonID {
				fail("push %d: I8: daemon_up headers wrong (Renotify must be false): %+v", i, p)
			}
			d.bannerShown = false
		default:
			fail("push %d: unknown push kind %q", i, p.Payload.Kind)
		}
	}
}

func (o *stormOracle) checkSessionPush(fail func(string, ...any), i int, p Push, now time.Time) {
	id := p.Payload.SessionID
	rec := o.sessions[id]
	if rec == nil {
		fail("push %d: I6: session push for %q, unseen or deleted", i, id)
		return
	}
	if rec.parent != "" {
		fail("push %d: I1: session push for subagent %q (parent %q)", i, id, rec.parent)
	}
	edge := p.Payload.State
	if edge == StateWorking {
		fail("push %d: I2: session push with state working", i)
	}
	if edge != StateWaiting && edge != StateReady && edge != StateError {
		fail("push %d: session push on a non-edge state %q", i, edge)
		return
	}
	if edge == StateReady {
		if rec.state != StateReady || now.Sub(rec.stateAt) < o.cfg.HoldDown {
			fail("push %d: I3: ready push for %q ready only %v (hold-down %v, state %q)",
				i, id, now.Sub(rec.stateAt), o.cfg.HoldDown, rec.state)
		}
	}
	var last time.Time
	switch edge {
	case StateReady:
		last = rec.ident.lastReadyPush
	case StateError:
		last = rec.ident.lastErrorPush
	default:
		last = rec.ident.lastWaitingPush
	}
	if !last.IsZero() && now.Sub(last) < o.cfg.Cooldown {
		fail("push %d: I4: %s push for %q only %v after the last (cooldown %v)", i, edge, id, now.Sub(last), o.cfg.Cooldown)
	}
	switch edge {
	case StateReady:
		rec.ident.lastReadyPush = now
	case StateError:
		rec.ident.lastErrorPush = now
	default:
		rec.ident.lastWaitingPush = now
	}

	inWindow := 1
	for _, pt := range o.pushTimes {
		if now.Sub(pt) < o.cfg.BurstWindow {
			inWindow++
		}
	}
	if inWindow > o.cfg.BurstThreshold {
		fail("push %d: I5: %d session pushes inside one %v window (threshold %d)", i, inWindow, o.cfg.BurstWindow, o.cfg.BurstThreshold)
	}
	o.pushTimes = append(o.pushTimes, now)

	wantTTL, wantUrgency := o.cfg.TTLWaiting, UrgencyHigh
	switch edge {
	case StateReady:
		wantTTL, wantUrgency = o.cfg.TTLReady, UrgencyNormal
	case StateError:
		wantTTL = o.cfg.TTLError
	}
	if p.TTL != wantTTL || p.Urgency != wantUrgency || !p.Renotify || p.Topic != id {
		fail("push %d: I8: %s push headers wrong: %+v", i, edge, p)
	}
}

func (o *stormOracle) checkSummaryPush(fail func(string, ...any), i int, p Push) {
	// The oracle cannot see cooldown-surviving candidates that were absorbed
	// into summaries, so the "distinct ids in the window" bound is graded
	// against its sound over-approximation: every id the trace has seen.
	if p.Payload.Count < 1 || p.Payload.Count > len(o.everSeen) {
		fail("push %d: I6: summary count %d outside 1..%d", i, p.Payload.Count, len(o.everSeen))
	}
	if len(p.Payload.Sessions) > maxSummarySessions {
		fail("push %d: summary carries %d labels, cap is %d", i, len(p.Payload.Sessions), maxSummarySessions)
	}
	if !sort.StringsAreSorted(p.Payload.Sessions) {
		fail("push %d: summary labels not sorted: %v", i, p.Payload.Sessions)
	}
	if p.TTL != o.cfg.TTLWaiting || p.Urgency != UrgencyHigh || p.Topic != summaryTopic {
		fail("push %d: I8: summary headers wrong: %+v", i, p)
	}
}

// applyEvent mirrors the engine's record bookkeeping — sighting, state,
// parent, delete, rekey move/merge, daemon up/down — with the same ignore
// rules for malformed input.
func (o *stormOracle) applyEvent(ev Event, now time.Time) {
	switch ev.Kind {
	case EventSessionUpdate:
		if ev.SessionID == "" {
			return
		}
		o.everSeen[ev.SessionID] = true
		rec := o.sessions[ev.SessionID]
		if rec == nil {
			o.sessions[ev.SessionID] = &oracleSession{ident: &oracleIdentity{}, state: ev.State, stateAt: now, parent: ev.ParentID}
			return
		}
		rec.parent = ev.ParentID
		if ev.State != rec.state {
			rec.state, rec.stateAt = ev.State, now
		}
	case EventSessionDelete:
		delete(o.sessions, ev.SessionID)
	case EventRekey:
		if ev.OldSessionID == "" || ev.SessionID == "" || ev.OldSessionID == ev.SessionID {
			return
		}
		old := o.sessions[ev.OldSessionID]
		if old == nil {
			return
		}
		delete(o.sessions, ev.OldSessionID)
		o.everSeen[ev.SessionID] = true
		dst := o.sessions[ev.SessionID]
		if dst == nil {
			o.sessions[ev.SessionID] = old
			return
		}
		// Mirror the engine's merge: newer state wins (tie keeps the
		// destination), same-state strands date the run from the earlier
		// change, cooldown stamps take the per-edge max.
		switch {
		case old.state == dst.state:
			if old.stateAt.Before(dst.stateAt) {
				dst.stateAt = old.stateAt
			}
		case old.stateAt.After(dst.stateAt):
			dst.state, dst.stateAt = old.state, old.stateAt
		}
		if old.ident.lastWaitingPush.After(dst.ident.lastWaitingPush) {
			dst.ident.lastWaitingPush = old.ident.lastWaitingPush
		}
		if old.ident.lastReadyPush.After(dst.ident.lastReadyPush) {
			dst.ident.lastReadyPush = old.ident.lastReadyPush
		}
		if old.ident.lastErrorPush.After(dst.ident.lastErrorPush) {
			dst.ident.lastErrorPush = old.ident.lastErrorPush
		}
	case EventDaemonDown:
		if ev.DaemonID == "" {
			return
		}
		d := o.daemons[ev.DaemonID]
		if d == nil {
			d = &oracleDaemon{}
			o.daemons[ev.DaemonID] = d
		}
		if d.downSince.IsZero() {
			d.downSince = now
		}
	case EventDaemonUp:
		if ev.DaemonID == "" {
			return
		}
		d := o.daemons[ev.DaemonID]
		if d == nil {
			o.daemons[ev.DaemonID] = &oracleDaemon{}
			return
		}
		d.downSince = time.Time{}
	}
}
