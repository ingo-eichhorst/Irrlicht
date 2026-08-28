package notify

import (
	"testing"
	"time"

	// Aliased: this package has its own `session` type.
	domain "irrlicht/core/domain/session"
)

// allStates is every State this package declares. It is hand-maintained, and
// that is the point: the test below is what makes it safe.
var allStates = []State{StateWorking, StateWaiting, StateReady, StateError}

// TestStateVocabularyMatchesTheDomain holds notify's copy of the lifecycle
// vocabulary against domain.CanonicalStates().
//
// The copy exists on purpose: notify imports nothing from the rest of the
// module (§5.1, ADR-5), so it re-declares the values rather than depending on
// the domain package. A TEST may import what the package may not — the
// architecture rule loads without Tests, and core/architecture_test.go says so
// where it is implemented — so the two lists can be compared even though the
// two packages cannot be coupled.
//
// It exists because the drift already happened. This engine was written
// against a three-state machine; the domain grew a fourth (#1796's `error`)
// and nothing anywhere noticed, so a session whose agent died mid-turn sent no
// notification at all. Nothing in the engine's own suite could have caught
// that: every test it had was written in the same three-state vocabulary.
func TestStateVocabularyMatchesTheDomain(t *testing.T) {
	canonical := domain.CanonicalStates()
	if len(canonical) == 0 {
		t.Fatal("domain.CanonicalStates() is empty — the comparison would be vacuous")
	}
	if len(allStates) != len(canonical) {
		t.Fatalf("notify declares %d states %v, the domain has %d %v", len(allStates), allStates, len(canonical), canonical)
	}
	// Order is the domain's own ladder and notify mirrors it, so a positional
	// comparison also pins that a new state was appended rather than spliced
	// into the middle of someone's switch.
	for i, want := range canonical {
		if string(allStates[i]) != want {
			t.Errorf("state %d: notify has %q, the domain has %q", i, allStates[i], want)
		}
	}
}

// TestEveryCanonicalStateHasADecidedPolicy fails on a state the engine has
// never been asked about. A new domain state defaults to the degrade-silent
// arm, which is correct for an UNKNOWN value off the wire and wrong for one
// the domain declares: `error` sat there for its whole life.
func TestEveryCanonicalStateHasADecidedPolicy(t *testing.T) {
	// notifies records the decision made for each state, in one place, so
	// adding a fifth is a compile-time-visible edit here rather than silence.
	notifies := map[State]bool{
		StateWorking: false, // entering work is never news (§8.4)
		StateWaiting: true,
		StateReady:   true,
		StateError:   true,
	}
	for _, st := range allStates {
		if _, ok := notifies[st]; !ok {
			t.Fatalf("state %q has no recorded policy: decide whether it notifies, do not let it fall to the degrade-silent arm", st)
		}
		e := New(Config{})
		wantNone(t, e.Handle(upd("s1", StateWorking), t0), "seed")
		// The transition plus a tick well past the hold-down: `ready` fires
		// from the tick rather than the edge, and this asks whether a state
		// notifies AT ALL, not when.
		n := len(e.Handle(upd("s1", st), t0.Add(time.Second)))
		n += len(e.Tick(t0.Add(time.Minute)))
		if notifies[st] && n != 1 {
			t.Errorf("state %q: recorded as notifying, but produced %d pushes", st, n)
		}
		if !notifies[st] && n != 0 {
			t.Errorf("state %q: recorded as silent, but produced %d pushes", st, n)
		}
	}
}
