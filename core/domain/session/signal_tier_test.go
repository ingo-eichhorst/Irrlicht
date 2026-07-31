package session

import "testing"

// TestSignalTier_Outranks pins the comparison semantics, and in particular the
// TierNone case that a bare `<` gets backwards: TierNone is numerically the
// lowest value but means "no tier known", so it must lose to every real tier
// rather than winning against all of them.
func TestSignalTier_Outranks(t *testing.T) {
	cases := []struct {
		name string
		a, b SignalTier
		want bool
	}{
		{"hook outranks transcript", TierHook, TierTranscript, true},
		{"transcript does not outrank hook", TierTranscript, TierHook, false},
		{"hook outranks structured", TierHook, TierStructured, true},
		{"structured outranks screen", TierStructured, TierScreen, true},
		{"equal tiers do not outrank", TierHook, TierHook, false},
		{"a real tier outranks TierNone", TierTranscript, TierNone, true},
		{"TierNone outranks nothing", TierNone, TierTranscript, false},
		{"TierNone does not outrank itself", TierNone, TierNone, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.a.Outranks(tc.b); got != tc.want {
				t.Errorf("%v.Outranks(%v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

// TestSignalTier_Known separates real tiers from the zero value and from any
// out-of-range value a future edit might introduce.
func TestSignalTier_Known(t *testing.T) {
	for _, tier := range []SignalTier{TierHook, TierStructured, TierProcess, TierScreen, TierTranscript} {
		if !tier.Known() {
			t.Errorf("tier %d must be Known", tier)
		}
	}
	if TierNone.Known() {
		t.Error("TierNone must not be Known")
	}
	if SignalTier(99).Known() {
		t.Error("an out-of-range tier must not be Known")
	}
}

// TestSignalTier_StringIsStable pins the wire strings. These are written into
// lifecycle.ClassifierInputs and read back by offline replay tooling, so they
// are an on-disk format: renaming one silently reinterprets every trace already
// recorded. TierNone deliberately renders empty so the omitempty field stays
// absent rather than recording a literal "none".
func TestSignalTier_StringIsStable(t *testing.T) {
	want := map[SignalTier]string{
		TierNone:       "",
		TierHook:       "hook",
		TierStructured: "structured",
		TierProcess:    "process",
		TierScreen:     "screen",
		TierTranscript: "transcript",
	}
	for tier, str := range want {
		if got := tier.String(); got != str {
			t.Errorf("SignalTier(%d).String() = %q, want %q", tier, got, str)
		}
	}
}
