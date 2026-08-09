package cliversion

import "testing"

func TestParse(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		// The four CLIs in play, exactly as they print today.
		{"2.1.226 (Claude Code)", "2.1.226", true}, // claude --version
		{"codex-cli 0.146.1", "0.146.1", true},     // codex --version
		{"1.0.26", "1.0.26", true},                 // copilot (#1355 Phase C)
		{"1.29.8", "1.29.8", true},                 // kiro-cli (#1355 Phase C)
		// Codex's transcript cli_version field and its release-tag spellings —
		// these came from the deleted parseCodexVersion's own table, so the
		// shared parser is held to everything the bespoke one handled.
		{"0.114.0", "0.114.0", true},
		{"rust-v0.114.0", "0.114.0", true},
		{"v0.113.0", "0.113.0", true},
		{"0.114.0-rc1", "0.114.0", true}, // pre-release suffix discarded
		{"0.114.0+build7", "0.114.0", true},
		// Unknown — every one of these must be reported as unparseable rather
		// than coerced to a number, because a wrong number compares.
		{"", "", false},
		{"garbage", "", false},
		{"0.114", "", false}, // two fields is not a version we will invent a patch for
		{"v", "", false},
	}
	for _, tc := range cases {
		got, ok := Parse(tc.in)
		if ok != tc.ok {
			t.Errorf("Parse(%q): ok=%v, want %v", tc.in, ok, tc.ok)
			continue
		}
		if ok && got.String() != tc.want {
			t.Errorf("Parse(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

func TestCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.0.1", "1.0.0", 1},
		{"1.0.0", "1.0.1", -1},
		{"1.1.0", "1.0.9", 1},
		{"2.0.0", "1.99.99", 1},
		{"0.113.9", "0.114.0", -1},
		// Field-wise, not lexicographic: "2.1.99" must not outrank "2.1.226".
		{"2.1.226", "2.1.99", 1},
	}
	for _, tc := range cases {
		a, _ := Parse(tc.a)
		b, _ := Parse(tc.b)
		if got := a.Compare(b); got != tc.want {
			t.Errorf("Parse(%q).Compare(%q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestAtLeast(t *testing.T) {
	cases := []struct {
		installed, minimum string
		wantOK, wantKnown  bool
	}{
		{"0.114.0", "0.114.0", true, true},  // exactly the floor is allowed
		{"0.114.1", "0.114.0", true, true},  // above by patch
		{"0.115.0", "0.114.0", true, true},  // above by minor
		{"1.0.0", "0.114.0", true, true},    // above by major
		{"0.113.9", "0.114.0", false, true}, // below by minor — the only refusing shape
		{"0.100.0", "0.114.0", false, true},
		{"2.1.121", "2.1.122", false, true},
		{"2.1.226", "2.1.122", true, true},
		// Unknown fails OPEN, in both directions. Each of these would silently
		// disable hooks for a real user if it flipped to closed.
		{"", "0.114.0", true, false},
		{"garbage", "0.114.0", true, false},
		{"0.114", "0.114.0", true, false},
		{"0.114.0", "", true, false},
		{"0.114.0", "not a version", true, false},
	}
	for _, tc := range cases {
		ok, known := AtLeast(tc.installed, tc.minimum)
		if ok != tc.wantOK || known != tc.wantKnown {
			t.Errorf("AtLeast(%q, %q) = (ok=%v, known=%v), want (ok=%v, known=%v)",
				tc.installed, tc.minimum, ok, known, tc.wantOK, tc.wantKnown)
		}
	}
}
