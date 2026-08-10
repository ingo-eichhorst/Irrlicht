package config

import (
	"testing"
	"time"
)

// TestEnvDurationRejectsRatherThanClamps pins the choice envDuration makes for
// a value that cannot have been meant. Honouring half of a typo is worse than
// ignoring it — the same call envInt makes for a negative.
func TestEnvDurationRejectsRatherThanClamps(t *testing.T) {
	const key = "IRRLICHT_TEST_DURATION"
	def := 30 * time.Minute

	cases := []struct {
		name string
		set  bool
		raw  string
		min  time.Duration
		want time.Duration
	}{
		{name: "unset takes the default", set: false, want: def},
		{name: "empty takes the default", set: true, raw: "", want: def},
		{name: "unparseable takes the default", set: true, raw: "soon", want: def},
		{name: "zero takes the default", set: true, raw: "0s", want: def},
		{name: "negative takes the default", set: true, raw: "-5m", want: def},
		{name: "a valid value wins", set: true, raw: "90s", want: 90 * time.Second},
		{name: "below min takes the default, not min", set: true, raw: "1ms", min: time.Second, want: def},
		{name: "exactly min is honoured", set: true, raw: "1s", min: time.Second, want: time.Second},
		{name: "above min is honoured", set: true, raw: "2s", min: time.Second, want: 2 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv(key, tc.raw)
			}
			if got := envDuration(key, def, tc.min); got != tc.want {
				t.Errorf("envDuration(%q=%q, def=%s, min=%s) = %s, want %s", key, tc.raw, def, tc.min, got, tc.want)
			}
		})
	}
}

// TestHookReverifyIntervalIsUnsetByDefault pins that config does NOT carry a
// second copy of the production cadence. services.NewHookEntryVerifier treats a
// non-positive interval as "take the default", and that is where the 5 minutes
// is written; a value here would be a duplicate that has to agree with it
// forever.
func TestHookReverifyIntervalIsUnsetByDefault(t *testing.T) {
	if got := Default().HookReverifyInterval; got != 0 {
		t.Errorf("HookReverifyInterval = %s with no env override, want 0 (defer to the verifier's own default)", got)
	}
}

// TestHookReverifyIntervalFloor: this timer can rewrite the user's agent config
// files, so an override below the floor must not turn the daemon into a write
// loop.
func TestHookReverifyIntervalFloor(t *testing.T) {
	t.Setenv("IRRLICHT_HOOK_REVERIFY_INTERVAL", "1ms")
	if got := Default().HookReverifyInterval; got != 0 {
		t.Errorf("HookReverifyInterval = %s for a 1ms override, want 0 (rejected, falls back to the verifier default)", got)
	}

	t.Setenv("IRRLICHT_HOOK_REVERIFY_INTERVAL", "2s")
	if got := Default().HookReverifyInterval; got != 2*time.Second {
		t.Errorf("HookReverifyInterval = %s for a 2s override, want 2s", got)
	}
}
