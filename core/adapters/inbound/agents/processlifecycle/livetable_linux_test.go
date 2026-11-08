//go:build linux

package processlifecycle

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"irrlicht/core/domain/agent"
)

// resetLiveTable clears the shared /proc table and restores the seams after t.
func resetLiveTable(t *testing.T) {
	t.Helper()
	prevNow, prevRead := liveTableNow, readLiveTable
	drop := func() {
		liveTableMu.Lock()
		liveTableAt, liveTableRows, liveTableErr = time.Time{}, nil, nil
		liveTableMu.Unlock()
	}
	drop()
	t.Cleanup(func() {
		liveTableNow, readLiveTable = prevNow, prevRead
		drop()
	})
}

func writeProcFile(t *testing.T, root string, pid, name, content string) {
	t.Helper()
	dir := filepath.Join(root, pid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadProcTable(t *testing.T) {
	root := t.TempDir()
	writeProcFile(t, root, "10", "cmdline", "/usr/local/bin/claude\x00--print\x00")
	writeProcFile(t, root, "10", "comm", "2.1.285\n")
	writeProcFile(t, root, "11", "comm", "renamed\n") // no cmdline: unreadable
	writeProcFile(t, root, "self", "comm", "ignored\n")

	rows, err := readProcTable(root)
	if err != nil {
		t.Fatal(err)
	}
	want := map[int]procEntry{
		10: {pid: 10, argv0: "claude", comm: "2.1.285", cmdline: "/usr/local/bin/claude --print", cmdlineOK: true},
		11: {pid: 11, comm: "renamed"},
	}
	if len(rows) != len(want) {
		t.Fatalf("rows = %+v, want %d entries", rows, len(want))
	}
	for _, r := range rows {
		if r != want[r.pid] {
			t.Errorf("pid %d: got %+v, want %+v", r.pid, r, want[r.pid])
		}
	}
}

func TestHasLiveFromTableMatchers(t *testing.T) {
	resetLiveTable(t)
	readLiveTable = func() ([]procEntry, error) {
		return []procEntry{
			{pid: 10, argv0: "claude", comm: "2.1.285", cmdline: "/usr/local/bin/claude --print", cmdlineOK: true},
			{pid: 11, comm: "renamed"},
			{pid: 12, argv0: "node", comm: "node", cmdline: "node /opt/codex/bin/codex.js", cmdlineOK: true},
			{pid: os.Getpid(), argv0: "self", comm: "self", cmdline: "only-the-daemon-itself", cmdlineOK: true},
		}, nil
	}
	tests := []struct {
		name string
		m    agent.ProcessMatcher
		want bool
	}{
		{"argv0 basename", agent.ExactName{Name: "claude"}, true},
		{"comm fallback", agent.ExactName{Name: "renamed"}, true},
		{"no such name", agent.ExactName{Name: "codex"}, false},
		{"empty name", agent.ExactName{Name: ""}, false},
		{"cmdline pattern", agent.CommandPattern{Regex: regexp.MustCompile(`codex\.js`)}, true},
		{"own pid excluded", agent.CommandPattern{Regex: regexp.MustCompile(`only-the-daemon`)}, false},
		{"unreadable cmdline skipped", agent.CommandPattern{Regex: regexp.MustCompile(`^$`)}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			live, ok := hasLiveFromTable(tt.m)
			if !ok {
				t.Fatal("real observer not recognised")
			}
			if live != tt.want {
				t.Errorf("live = %v, want %v", live, tt.want)
			}
		})
	}
}

func TestLiveTableWalksOncePerTTL(t *testing.T) {
	resetLiveTable(t)
	now := time.Unix(1_000, 0)
	liveTableNow = func() time.Time { return now }
	walks := 0
	readLiveTable = func() ([]procEntry, error) {
		walks++
		return []procEntry{{pid: 10, argv0: "claude", cmdlineOK: true}}, nil
	}
	ask := func() {
		HasLiveProcess(agent.ExactName{Name: "claude"})
		HasLiveProcess(agent.ExactName{Name: "codex"})
	}

	ask()
	now = now.Add(liveTableTTL / 2)
	ask()
	if walks != 1 {
		t.Fatalf("walks within TTL = %d, want 1", walks)
	}
	now = now.Add(liveTableTTL)
	ask()
	if walks != 2 {
		t.Fatalf("walks after TTL = %d, want 2", walks)
	}
}

// The table must answer exactly what the per-matcher observer path answers,
// on the real /proc.
func TestHasLiveProcessAgreesWithObserver(t *testing.T) {
	resetLiveTable(t)
	self := filepath.Base(os.Args[0])
	byName, err := osProc.FindByName(self)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := HasLiveProcess(agent.ExactName{Name: self}), len(byName) > 0; got != want {
		t.Errorf("ExactName(%q): table %v, observer %v", self, got, want)
	}
	pattern := regexp.QuoteMeta(os.Args[0])
	byCmd, err := osProc.FindByCmdline(pattern)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := HasLiveProcess(agent.CommandPattern{Regex: regexp.MustCompile(pattern)}), len(byCmd) > 0; got != want {
		t.Errorf("CommandPattern(%q): table %v, observer %v", pattern, got, want)
	}
}

// BenchmarkDetectionPass compares one detection pass over 16 adapters the old
// way (a /proc walk per matcher) with the shared table (one walk per pass).
func BenchmarkDetectionPass(b *testing.B) {
	names := []string{"claude", "codex", "aider", "gemini", "copilot", "opencode", "hermes", "junie",
		"kiro-cli", "vibe", "pi", "dsh", "muse", "antigravity", "kitty", "no-such-agent"}
	b.Run("per-matcher", func(b *testing.B) {
		for b.Loop() {
			for _, n := range names {
				_, _ = osProc.FindByName(n)
			}
		}
	})
	b.Run("shared-table", func(b *testing.B) {
		for b.Loop() {
			liveTableMu.Lock()
			liveTableAt = time.Time{}
			liveTableMu.Unlock()
			for _, n := range names {
				HasLiveProcess(agent.ExactName{Name: n})
			}
		}
	})
}
