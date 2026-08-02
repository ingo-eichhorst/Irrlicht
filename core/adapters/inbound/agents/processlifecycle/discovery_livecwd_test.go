package processlifecycle

import "testing"

// LiveCWDsByCmdline is the CommandPattern counterpart of LiveCWDs and is the
// real implementation behind hermes' project binding for CLI sessions, whose
// store rows carry no cwd of their own. Every hermes watcher test stubs the
// resolver out, so without this the load-bearing rules — dedupe, argv
// exclusion, empty-CWD tolerance — would have no coverage at all.
func TestLiveCWDsByCmdline(t *testing.T) {
	tests := []struct {
		name        string
		pids        []int
		cwd         map[int]string
		argv        map[int][]string
		excludeArgv func([]string) bool
		want        []string
	}{
		{
			name: "no matching processes yields an empty set",
			want: nil,
		},
		{
			name: "distinct cwds are all returned",
			pids: []int{101, 102},
			cwd:  map[int]string{101: "/work/a", 102: "/work/b"},
			want: []string{"/work/a", "/work/b"},
		},
		{
			// The rule hermes' defaultLiveCWD turns on: two processes in ONE
			// directory is still an unambiguous binding, two directories is not.
			name: "the same cwd twice collapses to one entry",
			pids: []int{101, 102},
			cwd:  map[int]string{101: "/work/a", 102: "/work/a"},
			want: []string{"/work/a"},
		},
		{
			name: "an unresolvable cwd is skipped, not returned as empty",
			pids: []int{101, 102},
			cwd:  map[int]string{101: "/work/a"}, // 102 → ""
			want: []string{"/work/a"},
		},
		{
			name: "excludeArgv drops a service process",
			pids: []int{101, 102},
			cwd:  map[int]string{101: "/work/a", 102: "/work/service"},
			argv: map[int][]string{
				101: {"/venv/bin/hermes", "chat"},
				102: {"/venv/bin/hermes", "gateway", "run"},
			},
			excludeArgv: func(argv []string) bool {
				for _, a := range argv {
					if a == "gateway" {
						return true
					}
				}
				return false
			},
			want: []string{"/work/a"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prev := osProc
			osProc = cmdlineObserver{fakeObserver{pids: tt.pids, cwd: tt.cwd, argv: tt.argv}}
			defer func() { osProc = prev }()

			got, err := LiveCWDsByCmdline("hermes", tt.excludeArgv)
			if err != nil {
				t.Fatalf("LiveCWDsByCmdline: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %v (%d entries), want %v", got, len(got), tt.want)
			}
			for _, w := range tt.want {
				if _, ok := got[w]; !ok {
					t.Errorf("missing %q from %v", w, got)
				}
			}
		})
	}
}

// An empty pattern is a no-op rather than a match-everything.
func TestLiveCWDsByCmdline_EmptyPattern(t *testing.T) {
	got, err := LiveCWDsByCmdline("", nil)
	if err != nil || got != nil {
		t.Errorf(`LiveCWDsByCmdline("") = %v, %v; want nil, nil`, got, err)
	}
}
