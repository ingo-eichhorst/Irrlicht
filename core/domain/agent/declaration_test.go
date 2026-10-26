package agent

import (
	"regexp"
	"strings"
	"testing"
)

// These tests assert compile-time that each declared variant satisfies its
// sealed sum interface. The sealed-sum guarantee — that only types in this
// package can implement ProcessMatcher / Source / FileParser — is enforced
// by the unexported marker methods (isProcessMatcher / isSource /
// isFileParser). A consumer in another package that tried to declare its
// own variant would fail to compile because it can't satisfy the
// unexported marker.

func TestProcessMatcherSatisfaction(t *testing.T) {
	var _ ProcessMatcher = ExactName{Name: "claude"}
	var _ ProcessMatcher = CommandPattern{Regex: regexp.MustCompile("/aider")}
}

func TestSourceSatisfaction(t *testing.T) {
	var _ Source = FilesUnderRoot{}
	var _ Source = FilesUnderCWD{}
	var _ Source = ProcessOwnedStore{}
}

func TestFileParserSatisfaction(t *testing.T) {
	var _ FileParser = JSONLineParser{}
	var _ FileParser = RawLineParser{}
}

// TestAgentZeroValue confirms an Agent{} zero-value compiles; useful for
// adapter authors who construct an Agent literal step-by-step.
func TestAgentZeroValue(t *testing.T) {
	var a Agent
	if a.Identity.Name != "" {
		t.Fatalf("zero-value Identity.Name should be empty, got %q", a.Identity.Name)
	}
	if a.Process.Match != nil {
		t.Fatalf("zero-value Process.Match should be nil interface")
	}
	if a.Source != nil {
		t.Fatalf("zero-value Source should be nil interface")
	}
}

// Issue #2082: the scanner mints no pre-session for a process that either
// ExcludeArgv rejects or SessionHostArgv recognizes; a nil predicate matches
// nothing.
func TestProcessSkipsPreSession(t *testing.T) {
	is := func(word string) func([]string) bool {
		return func(argv []string) bool { return len(argv) > 1 && argv[1] == word }
	}
	for _, tc := range []struct {
		name string
		p    Process
		argv []string
		want bool
	}{
		{"neither declared", Process{}, []string{"codex", "app-server"}, false},
		{"excluded", Process{ExcludeArgv: is("daemon")}, []string{"claude", "daemon"}, true},
		{"session host", Process{SessionHostArgv: is("app-server")}, []string{"codex", "app-server"}, true},
		{"both declared, host matches", Process{ExcludeArgv: is("daemon"), SessionHostArgv: is("app-server")}, []string{"codex", "app-server"}, true},
		{"both declared, neither matches", Process{ExcludeArgv: is("daemon"), SessionHostArgv: is("app-server")}, []string{"codex", "--yolo"}, false},
	} {
		if got := tc.p.SkipsPreSession(tc.argv); got != tc.want {
			t.Errorf("%s: SkipsPreSession(%q) = %t, want %t", tc.name, tc.argv, got, tc.want)
		}
	}
}

// TestSelectVariant walks SelectVariant over a two-artifact declaration shaped
// like opencode's (#2108) — [1.14.50, 2.0.0), a gap, [2.1.4, 3.0.0) — and over a
// single-artifact one, whose answers must be exactly VersionGate.Check's.
func TestSelectVariant(t *testing.T) {
	two := &ManagedUserFile{
		Version: &VersionGate{Min: "1.14.50", Max: "3.0.0"},
		Variants: []HookVariant{
			{Min: "1.14.50", Max: "2.0.0"},
			{Min: "2.1.4", Max: "3.0.0"},
		},
	}
	cases := []struct {
		installed string
		variant   int // index into two.Variants, -1 for none
		bound     VersionBound
		names     []string
	}{
		{"1.14.50", 0, WithinBounds, nil},
		{"1.99.99", 0, WithinBounds, nil},
		{"2.1.4", 1, WithinBounds, nil},
		{"2.99.0", 1, WithinBounds, nil},
		{"1.14.49", -1, BelowFloor, []string{"1.14.49", "1.14.50"}},
		{"2.0.0", -1, AtOrAboveCeiling, []string{"2.0.0", "2.1.4", "1.14.50 up to 2.0.0"}},
		{"2.1.3", -1, AtOrAboveCeiling, []string{"2.1.3", "2.0.0", "2.1.4"}},
		{"3.0.0", -1, AtOrAboveCeiling, []string{"3.0.0", "3.x"}},
		{"", -1, VersionUnknown, []string{"could not be learnt", "1.14.50 up to 2.0.0, 2.1.4 up to 3.0.0"}},
		{"2.1", -1, VersionUnknown, []string{`"2.1" is not a version`}},
	}
	for _, c := range cases {
		got, bound, why := two.SelectVariant(c.installed)
		var want *HookVariant
		if c.variant >= 0 {
			want = &two.Variants[c.variant]
		}
		if got != want || bound != c.bound {
			t.Errorf("SelectVariant(%q) = (%v, %d), want (%v, %d) — reason %q", c.installed, got, bound, want, c.bound, why)
		}
		for _, n := range c.names {
			if !strings.Contains(why, n) {
				t.Errorf("SelectVariant(%q) reason %q does not name %q", c.installed, why, n)
			}
		}
	}

	// A single-artifact declaration selects nothing and answers as Check does,
	// unknown failing OPEN — the direction every other adapter keeps.
	one := &ManagedUserFile{Version: &VersionGate{Min: "1.0.0", Max: "2.0.0"}}
	for _, v := range []string{"", "0.9.0", "1.5.0", "2.0.0"} {
		got, bound, why := one.SelectVariant(v)
		wantBound, wantWhy := one.Version.Check(v)
		if got != nil || bound != wantBound || why != wantWhy {
			t.Errorf("single-artifact SelectVariant(%q) = (%v, %d, %q), want (nil, %d, %q)", v, got, bound, why, wantBound, wantWhy)
		}
	}
}
