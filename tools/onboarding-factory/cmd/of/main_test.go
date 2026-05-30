package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// validRepo writes a minimal, schema-valid repo: a 2-scenario catalog and one
// recorded+assessed claudecode cell (so status shows the 3 pillars and validate
// passes).
func validRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write(t, filepath.Join(root, "replaydata", "agents", "scenarios.json"), `{
  "meta": {"min_versions": {"claudecode": "2.0.0"}},
  "scenarios": [
    {"id": "1.1", "name": "session-start", "description": "d", "process": "p", "acceptance_criteria": "a"},
    {"id": "2.1", "name": "basic-turn", "description": "d", "process": "p", "acceptance_criteria": "a"}
  ]
}`)
	cell := filepath.Join(root, "replaydata", "agents", "claudecode", "scenarios", "1-1_session-start")
	write(t, filepath.Join(cell, "metadata.json"), `{
  "scenario_id": "session-start",
  "artifacts": {"recordings": ["claudecode/scenarios/1-1_session-start/recordings/r1"]},
  "details": {"assessment": {"agent_supports": "yes", "daemon_capability": "full", "driver_capability": "ready"}}
}`)
	write(t, filepath.Join(cell, "expected.jsonl"), `{"schema_version":1}`+"\n")
	write(t, filepath.Join(cell, "recordings", "r1", "events.jsonl"), "\n")
	return root
}

func runOf(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := run(args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestStatusJSON(t *testing.T) {
	root := validRepo(t)
	code, out, errs := runOf("status", "--json", "--repo-root", root)
	if code != exitOK {
		t.Fatalf("exit=%d stderr=%s", code, errs)
	}
	var v statusView
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("bad json: %v\n%s", err, out)
	}
	if len(v.Scenarios) != 2 {
		t.Fatalf("want 2 scenarios, got %d", len(v.Scenarios))
	}
	// session-start cell carries the 3 pillars from the assessment.
	var ss *scenarioView
	for i := range v.Scenarios {
		if v.Scenarios[i].Name == "session-start" {
			ss = &v.Scenarios[i]
		}
	}
	if ss == nil {
		t.Fatal("session-start scenario missing")
	}
	c, ok := ss.Cells["claudecode"]
	if !ok {
		t.Fatal("claudecode cell missing")
	}
	if c.AgentSupports != "yes" || c.DaemonCapability != "full" || c.DriverCapability != "ready" {
		t.Fatalf("pillars wrong: %+v", c)
	}
	if !c.Recorded {
		t.Fatalf("session-start should be recorded: %+v", c)
	}
}

func TestStatusAgentFilter(t *testing.T) {
	root := validRepo(t)
	code, _, errs := runOf("status", "--agent", "nope", "--repo-root", root)
	if code != exitUsage {
		t.Fatalf("unknown agent should be usage error; exit=%d stderr=%s", code, errs)
	}
	code, out, _ := runOf("status", "--agent", "claudecode", "--json", "--repo-root", root)
	if code != exitOK {
		t.Fatalf("exit=%d", code)
	}
	var v statusView
	_ = json.Unmarshal([]byte(out), &v)
	if len(v.Agents) != 1 || v.Agents[0] != "claudecode" {
		t.Fatalf("agent filter not applied: %v", v.Agents)
	}
}

func TestStatusScenarioFilter(t *testing.T) {
	root := validRepo(t)
	code, out, _ := runOf("status", "--scenario", "basic-turn", "--json", "--repo-root", root)
	if code != exitOK {
		t.Fatalf("exit=%d", code)
	}
	var v statusView
	_ = json.Unmarshal([]byte(out), &v)
	if len(v.Scenarios) != 1 || v.Scenarios[0].Name != "basic-turn" {
		t.Fatalf("scenario filter not applied: %d", len(v.Scenarios))
	}
}

func TestStatusRuns(t *testing.T) {
	root := validRepo(t)
	// No run-log → graceful.
	code, out, _ := runOf("status", "--runs", "--repo-root", root)
	if code != exitOK || !strings.Contains(out, "no factory runs") {
		t.Fatalf("empty runs: exit=%d out=%q", code, out)
	}
	// With a run-log → JSON echoes the record.
	write(t, runLogPath(root), `{"id":"r1","started_at":"2026-05-30T00:00:00Z","verb":"record","agent":"claudecode","scenario":"session-start","outcome":"recorded"}`+"\n")
	code, out, _ = runOf("status", "--runs", "--json", "--repo-root", root)
	if code != exitOK {
		t.Fatalf("exit=%d", code)
	}
	var recs []RunRecord
	if err := json.Unmarshal([]byte(out), &recs); err != nil || len(recs) != 1 || recs[0].Outcome != "recorded" {
		t.Fatalf("run-log not echoed: %v / %s", err, out)
	}
}

func TestValidateClean(t *testing.T) {
	root := validRepo(t)
	code, out, _ := runOf("validate", "--repo-root", root)
	if code != exitOK {
		t.Fatalf("clean repo should validate; exit=%d out=%s", code, out)
	}
}

func TestValidateCatchesViolations(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(root string)
		want   string
	}{
		{"bad id", func(r string) {
			write(t, filepath.Join(r, "replaydata", "agents", "scenarios.json"),
				`{"meta":{"min_versions":{"claudecode":"2.0.0"}},"scenarios":[{"id":"x","name":"n","description":"d","process":"p","acceptance_criteria":"a"}]}`)
		}, "is not <section>.<index>"},
		{"unknown field", func(r string) {
			write(t, filepath.Join(r, "replaydata", "agents", "scenarios.json"),
				`{"meta":{"min_versions":{"claudecode":"2.0.0"}},"scenarios":[{"id":"1.1","name":"n","section":"S"}]}`)
		}, "unexpected field"},
		{"missing scenario_id", func(r string) {
			write(t, filepath.Join(r, "replaydata", "agents", "claudecode", "scenarios", "1-1_session-start", "metadata.json"),
				`{"artifacts":{"recordings":["x/r1"]}}`)
		}, "missing scenario_id"},
		{"dangling scenario_id", func(r string) {
			write(t, filepath.Join(r, "replaydata", "agents", "claudecode", "scenarios", "1-1_session-start", "metadata.json"),
				`{"scenario_id":"ghost","artifacts":{"recordings":["x/r1"]}}`)
		}, "not in the catalog"},
		{"recorded without expected", func(r string) {
			_ = os.Remove(filepath.Join(r, "replaydata", "agents", "claudecode", "scenarios", "1-1_session-start", "expected.jsonl"))
		}, "missing expected.jsonl"},
		{"orphan recording folder", func(r string) {
			write(t, filepath.Join(r, "replaydata", "agents", "claudecode", "scenarios", "9-9_orphan", "recordings", "r1", "events.jsonl"), "\n")
		}, "orphan recording folder"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := validRepo(t)
			tc.mutate(root)
			code, _, errs := runOf("validate", "--repo-root", root)
			if code != exitFail {
				t.Fatalf("want exitFail; exit=%d", code)
			}
			if !strings.Contains(errs, tc.want) {
				t.Fatalf("want %q in stderr, got:\n%s", tc.want, errs)
			}
		})
	}
}

func TestCoverageJSON(t *testing.T) {
	root := validRepo(t)
	code, out, errs := runOf("coverage", "--repo-root", root)
	if code != exitOK {
		t.Fatalf("exit=%d stderr=%s", code, errs)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("coverage not JSON: %v\n%s", err, out)
	}
	if _, ok := doc["scenarios"]; !ok {
		t.Fatalf("coverage missing scenarios: %s", out)
	}
}

func TestUsage(t *testing.T) {
	if code, _, _ := runOf(); code != exitUsage {
		t.Fatalf("no args should be usage error, got %d", code)
	}
	if code, _, _ := runOf("bogus"); code != exitUsage {
		t.Fatalf("unknown subcommand should be usage error, got %d", code)
	}
}
