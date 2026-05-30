package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScenarioAddThenValidate(t *testing.T) {
	root := validRepo(t)
	proc := filepath.Join(t.TempDir(), "p.md")
	_ = os.WriteFile(proc, []byte("1. do a thing\n"), 0o644)
	code, out, errs := runOf("scenario", "add", "--id", "9.1", "--name", "new-scn",
		"--description", "d", "--process-file", proc, "--repo-root", root)
	if code != exitOK {
		t.Fatalf("add failed: %d %s", code, errs)
	}
	if !strings.Contains(out, "new-scn ok") {
		t.Fatalf("unexpected out: %s", out)
	}
	// present + tree still valid
	if code, _, _ := runOf("validate", "--repo-root", root); code != exitOK {
		t.Fatal("validate failed after add")
	}
	b, _ := os.ReadFile(filepath.Join(root, "replaydata", "agents", "scenarios.json"))
	if !strings.Contains(string(b), `"new-scn"`) || !strings.Contains(string(b), "1. do a thing") {
		t.Fatalf("scenario not written with process: %s", b)
	}
}

func TestScenarioAddRejectsDupAndBadID(t *testing.T) {
	root := validRepo(t)
	if code, _, _ := runOf("scenario", "add", "--id", "1.1", "--name", "x", "--repo-root", root); code != exitFail {
		t.Fatal("dup id must fail")
	}
	if code, _, _ := runOf("scenario", "add", "--id", "bad", "--name", "y", "--repo-root", root); code != exitFail {
		t.Fatal("bad id must fail")
	}
	if code, _, _ := runOf("scenario", "add", "--id", "9.9", "--name", "session-start", "--repo-root", root); code != exitFail {
		t.Fatal("dup name must fail")
	}
}

func TestScenarioUpdate(t *testing.T) {
	root := validRepo(t)
	acc := filepath.Join(t.TempDir(), "a.md")
	_ = os.WriteFile(acc, []byte("- new crit\n"), 0o644)
	code, _, errs := runOf("scenario", "update", "--name", "session-start", "--acceptance-file", acc, "--repo-root", root)
	if code != exitOK {
		t.Fatalf("update failed: %d %s", code, errs)
	}
	b, _ := os.ReadFile(filepath.Join(root, "replaydata", "agents", "scenarios.json"))
	if !strings.Contains(string(b), "new crit") {
		t.Fatalf("update not applied: %s", b)
	}
	if code, _, _ := runOf("scenario", "update", "--name", "ghost", "--repo-root", root); code != exitFail {
		t.Fatal("update of missing scenario must fail")
	}
}

func TestAgentAdd(t *testing.T) {
	root := validRepo(t)
	code, _, errs := runOf("agent", "add", "--id", "newcli", "--name", "New CLI", "--provider", "acme",
		"--prereq", "set ACME_API_KEY", "--prereq", "install acme", "--repo-root", root)
	if code != exitOK {
		t.Fatalf("agent add failed: %d %s", code, errs)
	}
	var am agentMeta
	b, _ := os.ReadFile(filepath.Join(root, "replaydata", "agents", "newcli", "metadata.json"))
	if json.Unmarshal(b, &am) != nil || am.Provider != "acme" || len(am.Prerequisites) != 2 {
		t.Fatalf("agent metadata wrong: %s", b)
	}
	// registered as a column
	_, out, _ := runOf("status", "--json", "--repo-root", root)
	var v statusView
	_ = json.Unmarshal([]byte(out), &v)
	found := false
	for _, a := range v.Agents {
		if a == "newcli" {
			found = true
		}
	}
	if !found {
		t.Fatalf("newcli not registered as column: %v", v.Agents)
	}
	// idempotency guard
	if code, _, _ := runOf("agent", "add", "--id", "newcli", "--name", "x", "--provider", "y", "--repo-root", root); code != exitFail {
		t.Fatal("duplicate agent add must fail")
	}
}

func TestCellWriteFK(t *testing.T) {
	root := validRepo(t)
	cellFile := filepath.Join(t.TempDir(), "cell.json")
	_ = os.WriteFile(cellFile, []byte(`{"details":{"assessment":{"agent_supports":"yes"}}}`), 0o644)
	// dangling scenario → fail
	if code, _, _ := runOf("cell", "write", "--agent", "claudecode", "--scenario", "ghost", "--file", cellFile, "--repo-root", root); code != exitFail {
		t.Fatal("cell write with unknown scenario must fail")
	}
	// real scenario → ok, FK forced
	code, _, errs := runOf("cell", "write", "--agent", "claudecode", "--scenario", "basic-turn", "--file", cellFile, "--repo-root", root)
	if code != exitOK {
		t.Fatalf("cell write failed: %d %s", code, errs)
	}
	b, _ := os.ReadFile(filepath.Join(root, "replaydata", "agents", "claudecode", "scenarios", "2-1_basic-turn", "metadata.json"))
	if !strings.Contains(string(b), `"scenario_id": "basic-turn"`) {
		t.Fatalf("scenario_id FK not forced: %s", b)
	}
}

func TestCellSpec(t *testing.T) {
	root := validRepo(t)
	specFile := filepath.Join(t.TempDir(), "expected.jsonl")
	// meta line lacks scenario_id (FK forced on write) + one verbatim phase line
	// (well-formed per ParseShardSpec: phase name + exactly one of expected_state/kind).
	const phase = `{"phase":"birth","expected_state":"ready","relative_to":"start"}`
	_ = os.WriteFile(specFile, []byte(
		`{"schema_version":1,"notes":"a > b & c"}`+"\n"+phase+"\n"), 0o644)

	// dangling scenario → fail
	if code, _, _ := runOf("cell", "spec", "--agent", "claudecode", "--scenario", "ghost", "--file", specFile, "--repo-root", root); code != exitFail {
		t.Fatal("cell spec with unknown scenario must fail")
	}
	// real scenario → ok, FK forced on meta, phase line verbatim, meta NOT HTML-escaped
	code, _, errs := runOf("cell", "spec", "--agent", "claudecode", "--scenario", "basic-turn", "--file", specFile, "--repo-root", root)
	if code != exitOK {
		t.Fatalf("cell spec failed: %d %s", code, errs)
	}
	b, _ := os.ReadFile(filepath.Join(root, "replaydata", "agents", "claudecode", "scenarios", "2-1_basic-turn", "expected.jsonl"))
	if !strings.Contains(string(b), `"scenario_id":"basic-turn"`) {
		t.Fatalf("scenario_id FK not forced on meta line: %s", b)
	}
	if !strings.Contains(string(b), phase) {
		t.Fatalf("phase line not preserved verbatim: %s", b)
	}
	// Literal > and & survive: HTML-escaping would have written the > /
	// & forms, so a matching literal substring proves the meta line was
	// emitted unescaped (matching the rest of replaydata's style).
	if !strings.Contains(string(b), `a > b & c`) {
		t.Fatalf("meta notes HTML-escaped or not preserved: %s", b)
	}

	// malformed JSONL → fail
	bad := filepath.Join(t.TempDir(), "bad.jsonl")
	_ = os.WriteFile(bad, []byte("not json\n"), 0o644)
	if code, _, _ := runOf("cell", "spec", "--agent", "claudecode", "--scenario", "basic-turn", "--file", bad, "--repo-root", root); code != exitFail {
		t.Fatal("cell spec with malformed JSONL must fail")
	}
	// a `null` meta line must error cleanly, not panic the nil map
	nullMeta := filepath.Join(t.TempDir(), "null.jsonl")
	_ = os.WriteFile(nullMeta, []byte("null\n"), 0o644)
	if code, _, _ := runOf("cell", "spec", "--agent", "claudecode", "--scenario", "basic-turn", "--file", nullMeta, "--repo-root", root); code != exitFail {
		t.Fatal("cell spec with a null meta line must fail cleanly")
	}
	// a structurally-invalid phase (neither expected_state nor kind) is rejected at write time
	badPhase := filepath.Join(t.TempDir(), "badphase.jsonl")
	_ = os.WriteFile(badPhase, []byte(`{"schema_version":1}`+"\n"+`{"phase":"birth"}`+"\n"), 0o644)
	if code, _, _ := runOf("cell", "spec", "--agent", "claudecode", "--scenario", "basic-turn", "--file", badPhase, "--repo-root", root); code != exitFail {
		t.Fatal("cell spec with an invalid phase line must fail")
	}
}

func TestVerifyCommand(t *testing.T) {
	root := validRepo(t)
	cell := filepath.Join(root, "replaydata", "agents", "claudecode", "scenarios", "1-1_session-start")
	// validRepo's newest recording is "r1" (has a non-empty events.jsonl). Drop
	// the golden summary there so it's the one ValidateObservations reads.
	rec := filepath.Join(cell, "recordings", "r1")
	_ = os.WriteFile(filepath.Join(rec, "transcript.jsonl.replay.json.golden"),
		[]byte(`{"schema_version":1,"summary":{"estimated_cost_usd":0.1,"cum_input_tokens":5,"cum_output_tokens":5,"model_name":"claude-opus-4-7"}}`), 0o644)
	// expected.jsonl with a passing observations block + a trivially-passing state spec
	_ = os.WriteFile(filepath.Join(cell, "expected.jsonl"),
		[]byte(`{"schema_version":1,"scenario_id":"session-start","observations":{"model":"claude-opus-4-7","cost_nonzero":true}}`+"\n"), 0o644)

	code, out, errs := runOf("verify", "--agent", "claudecode", "--scenario", "session-start", "--repo-root", root)
	if code != exitOK {
		t.Fatalf("verify should pass: code=%d out=%s err=%s", code, out, errs)
	}
	if !strings.Contains(out, "observations: PASS") {
		t.Fatalf("expected observations PASS: %s", out)
	}

	// flip the asserted model → verify must fail
	_ = os.WriteFile(filepath.Join(cell, "expected.jsonl"),
		[]byte(`{"schema_version":1,"scenario_id":"session-start","observations":{"model":"WRONG"}}`+"\n"), 0o644)
	if code, _, _ := runOf("verify", "--agent", "claudecode", "--scenario", "session-start", "--repo-root", root); code != exitFail {
		t.Fatalf("verify must fail on model mismatch, got %d", code)
	}
}
