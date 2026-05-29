package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture writes a minimal shard repo under t.TempDir() (#510) and returns its
// root for --repo-root. It plants one non-terminal (assessed-not-recorded) cell
// so completeness fails, and one record_now/applicable:false contradiction so
// consistency fails — the same two failure shapes the legacy fixture used.
func fixture(t *testing.T) (repoRoot string) {
	t.Helper()
	tmp := t.TempDir()
	scen := filepath.Join(tmp, "replaydata", "scenarios")
	if err := os.MkdirAll(scen, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(scen, "_meta.json"), `{"min_versions":{"ag":"1.0.0"}}`)
	// recd: record_now assessment + recipe applicable:false → consistency contradiction.
	write(t, filepath.Join(scen, "recd.json"), `{
  "id": "1.1", "name": "recd", "section": "S", "feature": "Recd",
  "agents": {"ag": {"details": {
    "assessment": {"agent_supports":"yes","daemon_capability":"full","driver_capability":"ready"},
    "recipe": {"applicable": false}
  }}}
}`)
	// unrec: assessed recordable, no recording → completeness non-terminal.
	write(t, filepath.Join(scen, "unrec.json"), `{
  "id": "1.2", "name": "unrec", "section": "S", "feature": "Unrec",
  "agents": {"ag": {"details": {
    "assessment": {"agent_supports":"yes","daemon_capability":"full","driver_capability":"ready"},
    "recipe": {"script": [{"type":"send"}]}
  }}}
}`)
	return tmp
}

func TestRunUsageErrors(t *testing.T) {
	root := fixture(t)
	base := []string{"--repo-root", root}
	cases := []struct {
		name string
		args []string
		want int
	}{
		{"no subcommand", nil, exitUsage},
		{"wrong subcommand", []string{"frobnicate"}, exitUsage},
		{"cells without agent", append([]string{"query", "--cells"}, base...), exitUsage},
		{"completeness without agent", append([]string{"query", "--gate", "completeness"}, base...), exitUsage},
		{"completeness unknown agent", append([]string{"query", "--gate", "completeness", "--agent", "nope"}, base...), exitUsage},
		{"no mode", append([]string{"query"}, base...), exitUsage},
	}
	for _, c := range cases {
		var out, errb bytes.Buffer
		if got := run(c.args, &out, &errb); got != c.want {
			t.Errorf("%s: exit = %d; want %d (stderr: %s)", c.name, got, c.want, errb.String())
		}
	}
}

func TestRunCompleteness(t *testing.T) {
	root := fixture(t)
	var out, errb bytes.Buffer
	got := run([]string{"query", "--gate", "completeness", "--agent", "ag", "--repo-root", root}, &out, &errb)
	if got != exitFail {
		t.Fatalf("exit = %d; want %d (stderr: %s)", got, exitFail, errb.String())
	}
	if !strings.Contains(out.String(), "ok   recd") {
		t.Errorf("expected recd terminal on stdout, got:\n%s", out.String())
	}
	if !strings.Contains(errb.String(), "unrec") || !strings.Contains(errb.String(), "implement ag unrec") {
		t.Errorf("expected unrec GAP with implement hint on stderr, got:\n%s", errb.String())
	}
}

func TestRunConsistency(t *testing.T) {
	root := fixture(t)
	var out, errb bytes.Buffer
	got := run([]string{"query", "--gate", "consistency", "--repo-root", root}, &out, &errb)
	if got != exitFail {
		t.Fatalf("exit = %d; want %d (stderr: %s)", got, exitFail, errb.String())
	}
	if !strings.Contains(errb.String(), "ag/recd: assessment routes RECORD") {
		t.Errorf("expected the recd contradiction on stderr, got:\n%s", errb.String())
	}
}

func TestRunCells(t *testing.T) {
	root := fixture(t)
	var out, errb bytes.Buffer
	got := run([]string{"query", "--cells", "--agent", "ag", "--repo-root", root}, &out, &errb)
	if got != exitOK {
		t.Fatalf("exit = %d; want %d (stderr: %s)", got, exitOK, errb.String())
	}
	// Both applicable cells appear in the JSON.
	if !strings.Contains(out.String(), `"coverage_id": "recd"`) || !strings.Contains(out.String(), `"coverage_id": "unrec"`) {
		t.Errorf("expected both cells in JSON, got:\n%s", out.String())
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
