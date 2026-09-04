package main

// The six scenarios, one per row of docs/elfdans-device-test.md Phase 5 that
// needs an agent to be driven into a state. Each one invents the sessions it
// needs, drives the edges, and then states what
// docs/mobile-notifications-arc42.md §8.4 says must come back.
//
// Every timing below is read off notify.DefaultConfig() rather than retyped:
// the relay builds its engines with the zero notify.Config, which is that
// table, so a policy knob that moves takes these waits with it instead of
// leaving a driver that quietly waits for the wrong thing.

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"irrlicht/core/domain/notify"
	"irrlicht/core/domain/session"
)

// policy is §8.4's table as the relay applies it.
var policy = notify.DefaultConfig()

const (
	// beat separates two steps enough for a human reading the log to see
	// them as ordered. Nothing depends on it: frames travel one socket, in
	// order.
	beat = 250 * time.Millisecond

	// settle is how long a scenario waits for a push that policy says should
	// arrive at once. The whole path — hub, observer queue, engine,
	// dispatcher, encryption, POST — is local and sub-millisecond here, so
	// this is slack, not a measurement.
	settle = 4 * time.Second

	// slack absorbs scheduling jitter when a scenario asserts that a push
	// arrived no EARLIER than a timer allows. It is deliberately small: the
	// point of the ready row is that the banner does not appear instantly.
	slack = time.Second
)

// scenario is one Phase 5 row: what it drives, and what §8.4 owes back.
type scenario struct {
	name     string
	doc      string
	duration time.Duration
	run      func(ctx context.Context, e *env) error
	want     func(e *env, got []observed) error
}

var scenarios = map[string]scenario{
	"waiting": {
		name:     "waiting",
		doc:      "one session enters waiting — the push that has no hold-down (§8.4, latency is the feature)",
		duration: settle + 2*time.Second,
		run: func(ctx context.Context, e *env) error {
			id := e.sid(1)
			e.daemon.add(id, session.StateWorking, "")
			if err := e.daemon.connect(ctx); err != nil {
				return err
			}
			e.step("%s is working — a first sighting, which §8.4 records silently", id)
			e.sleep(ctx, beat)

			e.step("%s → waiting", id)
			if err := e.daemon.transition(id, session.StateWaiting); err != nil {
				return err
			}
			e.await(ctx, 1, settle)
			return ctx.Err()
		},
		want: func(e *env, got []observed) error {
			if err := wantCount(got, 1); err != nil {
				return err
			}
			return wantSession(got[0], notify.StateWaiting, e.sid(1))
		},
	},

	"ready": {
		name:     "ready",
		doc:      "one session finishes a turn — the push must wait out the 7 s hold-down, not fire instantly",
		duration: policy.HoldDown + settle + 2*time.Second,
		run: func(ctx context.Context, e *env) error {
			id := e.sid(1)
			e.daemon.add(id, session.StateWorking, "")
			if err := e.daemon.connect(ctx); err != nil {
				return err
			}
			e.sleep(ctx, beat)

			e.step("%s → ready (arms the %s hold-down)", id, policy.HoldDown)
			if err := e.daemon.transition(id, session.StateReady); err != nil {
				return err
			}
			e.mark("edge")
			e.await(ctx, 1, policy.HoldDown+settle)
			return ctx.Err()
		},
		want: func(e *env, got []observed) error {
			if err := wantCount(got, 1); err != nil {
				return err
			}
			if err := wantSession(got[0], notify.StateReady, e.sid(1)); err != nil {
				return err
			}
			// The row this scenario exists for is "banner after ~7s, not
			// instantly": a push that arrived immediately satisfies every
			// other assertion here and is the exact regression to catch.
			if held := got[0].Elapsed - e.marks["edge"]; held < policy.HoldDown-slack {
				return fmt.Errorf("the ready push arrived %s after the edge, inside the %s hold-down", held.Round(time.Millisecond), policy.HoldDown)
			}
			return nil
		},
	},

	"flap": {
		name:     "flap",
		doc:      "a finished turn that resumes inside the hold-down — no banner at all (§6.2)",
		duration: policy.HoldDown + settle + 4*time.Second,
		run: func(ctx context.Context, e *env) error {
			id := e.sid(1)
			e.daemon.add(id, session.StateWorking, "")
			if err := e.daemon.connect(ctx); err != nil {
				return err
			}
			e.sleep(ctx, beat)

			e.step("%s → ready (arms the %s hold-down)", id, policy.HoldDown)
			if err := e.daemon.transition(id, session.StateReady); err != nil {
				return err
			}
			e.sleep(ctx, policy.HoldDown/2)

			e.step("%s → working again, %s into the hold-down — this is the flap", id, policy.HoldDown/2)
			if err := e.daemon.transition(id, session.StateWorking); err != nil {
				return err
			}
			// Waiting the hold-down out is the whole assertion: a cancelled
			// hold-down and one that has not fired yet are indistinguishable
			// until the window has passed.
			e.quiet(ctx, policy.HoldDown+settle)
			return ctx.Err()
		},
		want: func(_ *env, got []observed) error { return wantCount(got, 0) },
	},

	"burst": {
		name:     "burst",
		doc:      "N sessions enter waiting inside the 20 s window — one summary, not N banners (-n, default 4)",
		duration: settle + 4*time.Second,
		run: func(ctx context.Context, e *env) error {
			if e.burst < 1 {
				return fmt.Errorf("-n must be at least 1, got %d", e.burst)
			}
			for i := 1; i <= e.burst; i++ {
				e.daemon.add(e.sid(i), session.StateWorking, "")
			}
			if err := e.daemon.connect(ctx); err != nil {
				return err
			}
			e.sleep(ctx, beat)

			// Every candidate past the threshold coalesces into the summary,
			// so the run has to fit inside BurstWindow — which is exactly the
			// row a tester cannot produce by hand with four real agents.
			e.step("%d sessions → waiting, inside the %s burst window", e.burst, policy.BurstWindow)
			for i := 1; i <= e.burst; i++ {
				if err := e.daemon.transition(e.sid(i), session.StateWaiting); err != nil {
					return err
				}
				e.sleep(ctx, beat)
			}
			e.await(ctx, e.burst, settle)
			return ctx.Err()
		},
		want: func(e *env, got []observed) error {
			// §8.4: the first BurstThreshold candidates push individually;
			// every one past it becomes a summary on the single "summary"
			// topic. So N candidates are still N pushes — the coalescing is
			// that all but the first three collapse onto ONE banner.
			individual := min(e.burst, policy.BurstThreshold)
			summaries := e.burst - individual
			if err := wantCount(got, e.burst); err != nil {
				return err
			}
			for i, p := range got[:individual] {
				if err := wantSession(p, notify.StateWaiting, e.sid(i+1)); err != nil {
					return err
				}
			}
			for _, p := range got[individual:] {
				if p.Payload.Kind != notify.PushSummary {
					return fmt.Errorf("expected a summary past the threshold of %d, got %s", policy.BurstThreshold, p)
				}
				if p.Topic != "summary" {
					return fmt.Errorf("a summary must collapse onto the summary topic, got %q", p.Topic)
				}
				// A summary is a waiting-shaped banner: it says agents need
				// you, so it outlives a ready and wakes the device (§8.4).
				if err := wantHeaders(p, policy.TTLWaiting, "high"); err != nil {
					return err
				}
			}
			if summaries == 0 {
				return nil
			}
			last := got[len(got)-1].Payload
			if last.Count != e.burst {
				return fmt.Errorf("the summary counts %d sessions, want %d", last.Count, e.burst)
			}
			return nil
		},
	},

	"subagent": {
		name:     "subagent",
		doc:      "a subagent enters waiting and must stay silent; only its parent notifies (§8.4)",
		duration: 2*settle + 4*time.Second,
		run: func(ctx context.Context, e *env) error {
			parent, child := e.sid(1), e.sid(2)
			e.daemon.add(parent, session.StateWorking, "")
			e.daemon.add(child, session.StateWorking, parent)
			if err := e.daemon.connect(ctx); err != nil {
				return err
			}
			e.sleep(ctx, beat)

			e.step("%s → waiting — a subagent of %s, so §8.4 says nothing at all", child, parent)
			if err := e.daemon.transition(child, session.StateWaiting); err != nil {
				return err
			}
			e.quiet(ctx, settle)

			e.step("%s → waiting — the parent, which does notify", parent)
			if err := e.daemon.transition(parent, session.StateWaiting); err != nil {
				return err
			}
			e.await(ctx, 1, settle)
			return ctx.Err()
		},
		want: func(e *env, got []observed) error {
			// One push, and it is the parent's: a relay that notified for the
			// subagent too would deliver two, and one that notified for the
			// subagent INSTEAD would deliver one with the wrong id.
			if err := wantCount(got, 1); err != nil {
				return err
			}
			return wantSession(got[0], notify.StateWaiting, e.sid(1))
		},
	},

	"disconnect": {
		name:     "disconnect",
		doc:      "the daemon link drops — one watchdog banner after the 60 s grace (§6.4)",
		duration: policy.DaemonGrace + settle + 8*time.Second,
		run: func(ctx context.Context, e *env) error {
			id := e.sid(1)
			e.daemon.add(id, session.StateWorking, "")
			if err := e.daemon.connect(ctx); err != nil {
				return err
			}
			e.sleep(ctx, beat)

			e.step("dropping the relay link and staying down — the %s grace starts now", policy.DaemonGrace)
			e.daemon.disconnect()
			e.mark("drop")
			e.await(ctx, 1, policy.DaemonGrace+settle+4*time.Second)
			return ctx.Err()
		},
		want: func(e *env, got []observed) error {
			if err := wantCount(got, 1); err != nil {
				return err
			}
			p := got[0].Payload
			if p.Kind != notify.PushDaemonDown {
				return fmt.Errorf("expected a %s push, got %s", notify.PushDaemonDown, got[0])
			}
			if p.DaemonID != e.daemon.id {
				return fmt.Errorf("the watchdog names daemon %q, want this driver's %q", p.DaemonID, e.daemon.id)
			}
			if err := wantHeaders(got[0], policy.TTLDaemon, "normal"); err != nil {
				return err
			}
			// §6.4's grace is the debounce that keeps a roaming reconnect
			// from buzzing the phone; a banner that beat it would mean the
			// watchdog fires on every network blip.
			if waited := got[0].Elapsed - e.marks["drop"]; waited < policy.DaemonGrace-slack {
				return fmt.Errorf("the watchdog banner arrived %s after the drop, inside the %s grace", waited.Round(time.Millisecond), policy.DaemonGrace)
			}
			return nil
		},
	},
}

// --- what a scenario asserts ---

func wantCount(got []observed, n int) error {
	if len(got) == n {
		return nil
	}
	return fmt.Errorf("the relay delivered %d push(es), want %d", len(got), n)
}

func wantSession(p observed, state notify.State, sessionID string) error {
	if p.Payload.Kind != notify.PushSession {
		return fmt.Errorf("expected a session push, got %s", p)
	}
	if p.Payload.SessionID != sessionID {
		return fmt.Errorf("the push names session %q, want %q", p.Payload.SessionID, sessionID)
	}
	if p.Payload.State != state {
		return fmt.Errorf("the push carries state %q, want %q", p.Payload.State, state)
	}
	// The device-side collapse key IS the session id (§8.4), and these ids
	// are short enough to ride in the Topic header unhashed — so a header
	// that does not name the session means the two collapse keys disagree.
	if p.Topic != sessionID {
		return fmt.Errorf("the Topic header is %q, want the session id %q", p.Topic, sessionID)
	}
	// §8.4's TTL split, and the urgency that goes with it: a stale ready is
	// noise, a waiting stays true.
	if state == notify.StateReady {
		return wantHeaders(p, policy.TTLReady, "normal")
	}
	return wantHeaders(p, policy.TTLWaiting, "high")
}

// wantHeaders checks the two RFC 8030 headers §8.4 assigns per push kind.
// They ride UNENCRYPTED — §8.2's privacy table names them as metadata the
// push service can read — and they are the half of a notification the phone
// never gets to correct: TTL decides how long an undelivered banner survives
// an offline phone (§5.2), Urgency whether it wakes one on battery (§5.3).
// Nothing else in the suite grades them against a live sender.
func wantHeaders(p observed, ttl time.Duration, urgency string) error {
	want := strconv.FormatInt(int64(ttl/time.Second), 10)
	if p.TTL != want {
		return fmt.Errorf("the TTL header is %q, want %q (%s)", p.TTL, want, ttl)
	}
	if p.Urgency != urgency {
		return fmt.Errorf("the Urgency header is %q, want %q", p.Urgency, urgency)
	}
	return nil
}

// --- scenario-facing helpers on env ---

// sid names one of this scenario's sessions. The scenario is in the name so
// a row that outlives a crashed run says which scenario invented it, and the
// whole id stays inside RFC 8030 §5.4's 32-character Topic limit, which is
// what lets wantSession compare the header to the id directly.
func (e *env) sid(n int) string {
	return fmt.Sprintf("bd-%s-%d", e.scenario.name, n)
}

func (e *env) step(format string, a ...any) {
	fmt.Printf("\033[36m--\033[0m +%-6s %s\n", fmt.Sprintf("%.1fs", e.elapsed().Seconds()), fmt.Sprintf(format, a...))
}

// mark records when a scenario did something a later assertion measures
// from, on the same clock the observer stamps arrivals with.
func (e *env) mark(name string) {
	e.marks[name] = e.elapsed()
}

func (e *env) elapsed() time.Duration { return time.Since(e.start) }

// clearBurstWindow waits out any candidates a PREVIOUS run of this driver
// left in the workspace's burst window. §8.4's window is per workspace and
// 20 s wide, and its entries deliberately survive the session deletes this
// driver performs on the way out — they are history, and a delete cannot
// unmake the load they represent. So two runs inside twenty seconds are
// graded on each other's candidates: a lone waiting edge comes back as a
// summary, and a burst counts one session too many. The wait is only ever
// the remainder, and it is nothing at all after a pause.
//
// It can only account for THIS driver's runs. A real session on the same
// relay and in the same workspace that pushed within the window counts
// toward these scenarios too, and no driver can wait that out — which is why
// the device test's Phase 5 wants a quiet relay.
func (e *env) clearBurstWindow(ctx context.Context, lastRun int64) {
	if lastRun == 0 {
		return
	}
	remaining := policy.BurstWindow - time.Since(time.Unix(lastRun, 0))
	if remaining <= 0 {
		return
	}
	e.step("waiting out %s of the %s burst window left by the previous run", remaining.Round(time.Second), policy.BurstWindow)
	e.sleep(ctx, remaining)
}

func (e *env) sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// await waits for n pushes, returning as soon as they arrive. Without an
// observer there is nothing to wait for, so it waits out the whole window
// rather than pretending the scenario finished early.
func (e *env) await(ctx context.Context, n int, within time.Duration) {
	if e.observer == nil {
		e.sleep(ctx, within)
		return
	}
	e.observer.await(ctx, n, within)
}

// quiet waits out a window in which policy says nothing should arrive.
func (e *env) quiet(ctx context.Context, d time.Duration) {
	if e.observer == nil {
		e.sleep(ctx, d)
		return
	}
	e.observer.quiet(ctx, d)
}
