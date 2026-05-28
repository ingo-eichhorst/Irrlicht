package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture writes a minimal scenarios.json + one agent's capabilities and
// assessments under t.TempDir(), returning the override flags. It plants one
// non-terminal (assessed-not-recorded) cell so completeness fails, and one
// record_now/applicable:false contradiction so consistency fails.
func fixture(t *testing.T) (scenarios, agentsRoot string) {
	t.Helper()
	tmp := t.TempDir()
	scenarios = filepath.Join(tmp, "scenarios.json")
	write(t, scenarios, `{"catalog":[{"id":"recd"},{"id":"unrec"}],
 "scenarios":[
   {"name":"recd","coverage_id":"recd","requires":[],"by_adapter":{"ag":{"applicable":false}}},
   {"name":"unrec","coverage_id":"unrec","requires":[],"by_adapter":{"ag":{"script":[{"type":"send"}]}}}
 ]}`)
	agentsRoot = filepath.Join(tmp, "agents")
	write(t, filepath.Join(agentsRoot, "ag", "capabilities.json"), `{"agent":"ag","transport":"line_based","features":{}}`)
	// recd: record_now assessment + applicable:false → consistency contradiction.
	write(t, filepath.Join(agentsRoot, "ag", "scenarios", "recd", "assessment.json"),
		`{"agent_supports":"yes","daemon_capability":"full","driver_capability":"ready"}`)
	// unrec: assessed recordable, no recording → completeness non-terminal.
	write(t, filepath.Join(agentsRoot, "ag", "scenarios", "unrec", "assessment.json"),
		`{"agent_supports":"yes","daemon_capability":"full","driver_capability":"ready"}`)
	return scenarios, agentsRoot
}

func TestRunUsageErrors(t *testing.T) {
	scenarios, agentsRoot := fixture(t)
	base := []string{"--scenarios", scenarios, "--agents-root", agentsRoot}
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
	scenarios, agentsRoot := fixture(t)
	var out, errb bytes.Buffer
	got := run([]string{"query", "--gate", "completeness", "--agent", "ag", "--scenarios", scenarios, "--agents-root", agentsRoot}, &out, &errb)
	if got != exitFail {
		t.Fatalf("exit = %d; want %d", got, exitFail)
	}
	if !strings.Contains(out.String(), "ok   recd") {
		t.Errorf("expected recd terminal on stdout, got:\n%s", out.String())
	}
	if !strings.Contains(errb.String(), "unrec") || !strings.Contains(errb.String(), "implement ag unrec") {
		t.Errorf("expected unrec GAP with implement hint on stderr, got:\n%s", errb.String())
	}
}

func TestRunConsistency(t *testing.T) {
	scenarios, agentsRoot := fixture(t)
	var out, errb bytes.Buffer
	got := run([]string{"query", "--gate", "consistency", "--scenarios", scenarios, "--agents-root", agentsRoot}, &out, &errb)
	if got != exitFail {
		t.Fatalf("exit = %d; want %d (stderr: %s)", got, exitFail, errb.String())
	}
	if !strings.Contains(errb.String(), "ag/recd: assessment routes RECORD") {
		t.Errorf("expected the recd contradiction on stderr, got:\n%s", errb.String())
	}
}

func TestRunCells(t *testing.T) {
	scenarios, agentsRoot := fixture(t)
	var out, errb bytes.Buffer
	got := run([]string{"query", "--cells", "--agent", "ag", "--scenarios", scenarios, "--agents-root", agentsRoot}, &out, &errb)
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
