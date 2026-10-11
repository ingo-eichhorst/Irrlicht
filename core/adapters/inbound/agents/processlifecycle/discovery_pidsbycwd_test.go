package processlifecycle

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// cwdErrObserver is fakeObserver with one PID whose cwd cannot be read, and a
// record of which PIDs had their cwd read.
type cwdErrObserver struct {
	fakeObserver
	unreadable int
	cwdReads   *[]int
}

func (o cwdErrObserver) CWDOf(pid int) (string, error) {
	*o.cwdReads = append(*o.cwdReads, pid)
	if pid == o.unreadable {
		return "", errors.New("lsof timed out")
	}
	return o.fakeObserver.CWDOf(pid)
}

// PIDsByCWDExcludingArgv is the scan codex's LauncherPID counts TUIs with
// (#2083): every same-name process in the cwd that the argv predicate keeps.
func TestPIDsByCWDExcludingArgv(t *testing.T) {
	real := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(real); err == nil {
		real = resolved // macOS: /var → /private/var, as CWDOf reports it
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	const (
		host      = 100 // app-server, in the cwd
		tuiA      = 200
		tuiB      = 300
		elsewhere = 400
	)
	isHost := func(argv []string) bool { return slices.Contains(argv, "app-server") }
	base := fakeObserver{
		pids: []int{host, tuiA, tuiB, elsewhere, os.Getpid()},
		cwd: map[int]string{host: real, tuiA: real, tuiB: real, elsewhere: "/somewhere/else",
			os.Getpid(): real},
		argv: map[int][]string{host: {"codex", "app-server"}, tuiA: {"codex"}, tuiB: {"codex"},
			elsewhere: {"codex"}, os.Getpid(): {"codex"}},
	}

	for _, tc := range []struct {
		name       string
		cwd        string
		unreadable int
		want       []int
		wantErr    bool
	}{
		// Both TUIs, not one: the caller decides what a count means. The
		// app-server and this process are dropped; the symlinked spelling of
		// the cwd matches the canonical one CWDOf reports.
		{name: "every match", cwd: link, want: []int{tuiA, tuiB}},
		{name: "canonical cwd", cwd: real, want: []int{tuiA, tuiB}},
		// A skipped match would read as one TUI fewer.
		{name: "unreadable cwd", cwd: real, unreadable: tuiB, wantErr: true},
		{name: "no cwd", cwd: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var reads []int
			prev := osProc
			osProc = cwdErrObserver{fakeObserver: base, unreadable: tc.unreadable, cwdReads: &reads}
			defer func() { osProc = prev }()

			got, err := PIDsByCWDExcludingArgv("codex", tc.cwd, isHost)

			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want error %t", err, tc.wantErr)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("pids = %v, want %v", got, tc.want)
			}
			// The argv filter runs before the cwd read, so an excluded
			// process costs no lsof.
			if slices.Contains(reads, host) {
				t.Errorf("read the cwd of the excluded app-server (cwd reads %v)", reads)
			}
		})
	}
}
