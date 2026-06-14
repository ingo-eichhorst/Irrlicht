package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// recordRepo extends validRepo with an agent driver + a stub run-cell.sh so the
// record resolver has something to find.
func recordRepo(t *testing.T) string {
	t.Helper()
	root := validRepo(t)
	write(t, filepath.Join(root, "replaydata", "agents", "claudecode", "driver-interactive.sh"), "#!/usr/bin/env bash\n")
	write(t, filepath.Join(root, "tools", "onboarding-factory", "scripts", "run-cell.sh"), "#!/usr/bin/env bash\n")
	return root
}

func TestRecordRunDryRun(t *testing.T) {
	root := recordRepo(t)
	code, out, errs := runOf("record", "run", "--agent", "claudecode", "--scenario", "session-start", "--dry-run", "--repo-root", root)
	if code != exitOK {
		t.Fatalf("dry-run exit=%d err=%s", code, errs)
	}
	if !strings.Contains(out, "driver-interactive.sh") || !strings.Contains(out, "run-cell.sh") {
		t.Fatalf("dry-run plan missing driver/run-cell: %s", out)
	}
	if !strings.Contains(out, "claudecode session-start") {
		t.Fatalf("dry-run command missing args: %s", out)
	}
}

func TestRecordRunMissingDriver(t *testing.T) {
	root := validRepo(t) // no driver written
	write(t, filepath.Join(root, "tools", "onboarding-factory", "scripts", "run-cell.sh"), "#!/usr/bin/env bash\n")
	code, _, errs := runOf("record", "run", "--agent", "claudecode", "--scenario", "session-start", "--dry-run", "--repo-root", root)
	if code != exitFail || !strings.Contains(errs, "no driver") {
		t.Fatalf("missing driver should fail with 'no driver'; exit=%d err=%s", code, errs)
	}
}

func TestRecordRunUnknownScenario(t *testing.T) {
	root := recordRepo(t)
	if code, _, _ := runOf("record", "run", "--agent", "claudecode", "--scenario", "ghost", "--dry-run", "--repo-root", root); code != exitFail {
		t.Fatal("unknown scenario must fail")
	}
}

func TestRecordPrereqCheck(t *testing.T) {
	root := recordRepo(t)
	// no metadata.json → graceful
	code, out, _ := runOf("record", "prereq-check", "--agent", "claudecode", "--repo-root", root)
	if code != exitOK || !strings.Contains(out, "no recording prerequisites") {
		t.Fatalf("missing metadata should be graceful: %d %s", code, out)
	}
	// with prerequisites → listed
	write(t, filepath.Join(root, "replaydata", "agents", "claudecode", "metadata.json"),
		`{"id":"claudecode","name":"Claude Code","provider":"anthropic","prerequisites":["switch to API key","set ANTHROPIC_API_KEY"]}`)
	code, out, _ = runOf("record", "prereq-check", "--agent", "claudecode", "--repo-root", root)
	if code != exitOK || !strings.Contains(out, "switch to API key") || !strings.Contains(out, "set ANTHROPIC_API_KEY") {
		t.Fatalf("prereqs not listed: %d %s", code, out)
	}
}

func TestRecordPrereqBudget(t *testing.T) {
	root := recordRepo(t)
	// An assessed-but-unrecorded claudecode cell for basic-turn (2 wait_turns)
	// → pending-record, so prereq-check estimates the recording budget.
	cell := filepath.Join(root, "replaydata", "agents", "claudecode", "scenarios", "2-1_basic-turn")
	write(t, filepath.Join(cell, "metadata.json"), `{"scenario_id":"basic-turn","details":{`+
		`"assessment":{"agent_supports":"yes","daemon_capability":"full","driver_capability":"ready"},`+
		`"recipe":{"timeout_seconds":120,"settings":{},"script":[`+
		`{"type":"send","text":"hi"},{"type":"wait_turn"},{"type":"send","text":"more"},{"type":"wait_turn"}]}}}`)
	code, out, _ := runOf("record", "prereq-check", "--agent", "claudecode", "--repo-root", root)
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	if !strings.Contains(out, "1 cell(s) pending-record") || !strings.Contains(out, "~2 agent request") {
		t.Fatalf("budget estimate missing/wrong: %s", out)
	}
}

func TestRecordVerifyAlias(t *testing.T) {
	root := recordRepo(t)
	// give the session-start cell a golden + observations so verify has something
	cell := filepath.Join(root, "replaydata", "agents", "claudecode", "scenarios", "1-1_session-start")
	_ = os.WriteFile(filepath.Join(cell, "recordings", "r1", "transcript.jsonl.replay.json.golden"),
		[]byte(`{"schema_version":1,"summary":{"estimated_cost_usd":0.1,"model_name":"claude-opus-4-7"}}`), 0o644)
	_ = os.WriteFile(filepath.Join(cell, "expected.jsonl"),
		[]byte(`{"schema_version":1,"scenario_id":"session-start","observations":{"model":"claude-opus-4-7","cost_nonzero":true}}`+"\n"), 0o644)
	code, out, errs := runOf("record", "verify", "--agent", "claudecode", "--scenario", "session-start", "--repo-root", root)
	if code != exitOK {
		t.Fatalf("record verify should pass: %d %s %s", code, out, errs)
	}
	if !strings.Contains(out, "observations: PASS") {
		t.Fatalf("expected observations PASS: %s", out)
	}
}
