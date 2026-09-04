package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	desktopResultsFile = "execution-results.json"
	desktopHookReceipt = `{"seq":2,"ts":"2026-05-01T00:00:01Z","kind":"hook_received","session_id":"transcript-1","hook_name":"Stop"}`
)

type desktopFixture struct {
	root     string
	cellDirs map[string]string
	record   string
}

type observedResultFixture struct {
	scenario string
	outcome  string
	reason   string
}

type nonObservedResultFixture struct {
	scenario       string
	outcome        string
	reason         string
	missingControl string
}

// desktopResultsRepo is the positive fixture for the full result vocabulary.
// It deliberately uses five different cells so completeness, uniqueness, and
// each outcome are proven together instead of by disconnected partial trees.
func desktopResultsRepo(t *testing.T, enforce bool) desktopFixture {
	t.Helper()
	root := t.TempDir()
	meta := `"min_versions":{"claudecode":"2.0.0"}`
	if enforce {
		meta += `,"required_execution_profiles_by_agent":{"claudecode":["desktop-local"]}`
	}
	write(t, filepath.Join(root, "replaydata", "agents", "scenarios.json"), `{
  "meta": {`+meta+`},
  "scenarios": [
    {"id":"1.1","name":"observed-pass","description":"d","process":"p","acceptance_criteria":"a"},
    {"id":"1.2","name":"observed-failure","description":"d","process":"p","acceptance_criteria":"a"},
    {"id":"1.3","name":"not-applicable","description":"d","process":"p","acceptance_criteria":"a"},
    {"id":"1.4","name":"unobservable","description":"d","process":"p","acceptance_criteria":"a"},
    {"id":"1.5","name":"not-runnable","description":"d","process":"p","acceptance_criteria":"a"}
  ]
}`)

	fixture := desktopFixture{root: root, cellDirs: map[string]string{}, record: "desktop-r1"}
	for i, scenario := range []string{"observed-pass", "observed-failure", "not-applicable", "unobservable", "not-runnable"} {
		folder := "1-" + string(rune('1'+i)) + "_" + scenario
		cell(t, root, "claudecode", folder, "yes", "full", "ready")
		fixture.cellDirs[scenario] = filepath.Join(root, "replaydata", "agents", "claudecode", "scenarios", folder)
	}

	writeObservedDesktopResult(t, fixture, observedResultFixture{scenario: "observed-pass", outcome: "observed-passing"})
	writeObservedDesktopResult(t, fixture, observedResultFixture{scenario: "observed-failure", outcome: "observed-failure", reason: "Irrlicht reported the observed failure."})
	writeNonObservedDesktopResult(t, fixture, nonObservedResultFixture{scenario: "not-applicable", outcome: "not-applicable", reason: "The scenario is outside Local Desktop."})
	writeNonObservedDesktopResult(t, fixture, nonObservedResultFixture{scenario: "unobservable", outcome: "unobservable", reason: "The local runtime leaves no observable trace."})
	writeNonObservedDesktopResult(t, fixture, nonObservedResultFixture{scenario: "not-runnable", outcome: "not-runnable", reason: "The Desktop driver cannot perform this recipe.", missingControl: "model selector"})
	return fixture
}

func writeObservedDesktopResult(t *testing.T, fixture desktopFixture, spec observedResultFixture) {
	t.Helper()
	cellDir := fixture.cellDirs[spec.scenario]
	recDir := filepath.Join(cellDir, "recordings", fixture.record)
	recording(t, fixture.root, "claudecode", filepath.Base(cellDir), fixture.record, true)
	write(t, filepath.Join(recDir, "manifest.json"), `{
  "execution_profile":"desktop-local",
  "entrypoint":"claude-desktop",
  "daemon_version":"0.6.2+fixture",
  "agent_cli_version":"2.1.258",
  "desktop_app_version":"1.44121.4"
}`)
	write(t, filepath.Join(recDir, "transcript.jsonl"), `{"type":"user","sessionId":"transcript-1","cwd":"/workspace","entrypoint":"claude-desktop"}`+"\n")
	// A measured project-local Claude Desktop registry row omits envScopeId.
	write(t, filepath.Join(recDir, "desktop-registry.json"), `{"sessionId":"local_desktop-1","cliSessionId":"transcript-1","cwd":"/workspace"}`+"\n")
	write(t, filepath.Join(recDir, "desktop-environment.json"), `{"selected_environment":"Local","requested_workspace":"/workspace"}`+"\n")
	write(t, filepath.Join(recDir, "irrlicht-session.json"), `{"session_id":"transcript-1","cwd":"/workspace","pid":4242,"launcher":{"host_bundle_id":"com.anthropic.claudefordesktop"}}`+"\n")
	// hooks.jsonl preserves the exact hook-receipt rows extracted from the
	// recording's events.jsonl. It is not the raw inbound Claude hook payload.
	write(t, filepath.Join(recDir, "events.jsonl"),
		`{"seq":1,"ts":"2026-05-01T00:00:00Z","kind":"transcript_new","session_id":"transcript-1","adapter":"claudecode"}`+"\n"+
			desktopHookReceipt+"\n")
	write(t, filepath.Join(recDir, "hooks.jsonl"), desktopHookReceipt+"\n")
	write(t, filepath.Join(recDir, "process.json"), `{"pid":4242,"command":"claude"}`+"\n")
	writeExpectedOutcome(t, cellDir, spec.scenario, spec.outcome)

	result := map[string]any{
		"scenario_id":       spec.scenario,
		"execution_profile": "desktop-local",
		"outcome":           spec.outcome,
		"recording":         fixture.record,
		"evidence": map[string]string{
			"desktop_registry": "desktop-registry.json",
			"transcript":       "transcript.jsonl",
			"hooks":            "hooks.jsonl",
			"process":          "process.json",
			"irrlicht_session": "irrlicht-session.json",
			"environment":      "desktop-environment.json",
		},
	}
	if spec.reason != "" {
		result["reason"] = spec.reason
	}
	writeExecutionResults(t, cellDir, []any{result})
}

func writeNonObservedDesktopResult(t *testing.T, fixture desktopFixture, spec nonObservedResultFixture) {
	t.Helper()
	cellDir := fixture.cellDirs[spec.scenario]
	evidencePath := filepath.Join(cellDir, "desktop-evidence", spec.scenario+".md")
	write(t, evidencePath, "Desktop campaign evidence for "+spec.scenario+".\n")
	result := map[string]any{
		"scenario_id":       spec.scenario,
		"execution_profile": "desktop-local",
		"outcome":           spec.outcome,
		"reason":            spec.reason,
		"evidence_refs": []string{
			filepath.ToSlash(strings.TrimPrefix(evidencePath, fixture.root+string(filepath.Separator))),
		},
	}
	if spec.missingControl != "" {
		result["missing_control"] = spec.missingControl
	}
	writeExecutionResults(t, cellDir, []any{result})
}

func writeExpectedOutcome(t *testing.T, cellDir, scenario, outcome string) {
	t.Helper()
	phase := `{"phase":"birth","kind":"transcript_new","relative_to":"start","max_delay_ms":1000,"text":"the recording contains a transcript"}`
	if outcome == "observed-failure" {
		phase = `{"phase":"failure","expected_state":"error","relative_to":"start","max_delay_ms":1000,"text":"the expected failure is visible"}`
	}
	write(t, filepath.Join(cellDir, "expected.jsonl"),
		`{"schema_version":1,"scenario_id":"`+scenario+`","source":"desktop fixture"}`+"\n"+phase+"\n")
}

func writeExecutionResults(t *testing.T, cellDir string, results []any) {
	t.Helper()
	b, err := json.MarshalIndent(map[string]any{"schema_version": 1, "results": results}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(cellDir, desktopResultsFile), string(b)+"\n")
}

func requireDesktopFinding(t *testing.T, root, scenario string, fields ...string) {
	t.Helper()
	code, _, stderr := runOf("validate", "--repo-root", root)
	if code != exitFail {
		t.Fatalf("of validate accepted the Desktop evidence mutation; exit=%d stderr=%s", code, stderr)
	}
	for _, want := range append([]string{scenario}, fields...) {
		if !strings.Contains(stderr, want) {
			t.Fatalf("finding must name %q; stderr:\n%s", want, stderr)
		}
	}
}

func TestValidateDesktopResultsAcceptsAllOutcomes(t *testing.T) {
	fixture := desktopResultsRepo(t, true)
	if code, _, stderr := runOf("validate", "--repo-root", fixture.root); code != exitOK {
		t.Fatalf("complete Desktop result fixture failed: exit=%d stderr:\n%s", code, stderr)
	}
}

// These are compatibility locks. The first protects today's CLI-only tree.
// The second lets independent campaign PRs add partial Desktop results before
// the catalog explicitly enables the exact-set completeness gate.
func TestValidateDesktopResultsCompatibilityBeforeEnablement(t *testing.T) {
	if code, _, stderr := runOf("validate", "--repo-root", validRepo(t)); code != exitOK {
		t.Fatalf("CLI-only repository must stay valid: exit=%d stderr=%s", code, stderr)
	}
	fixture := desktopResultsRepo(t, false)
	delete(fixture.cellDirs, "not-runnable")
	write(t, filepath.Join(fixture.root, "replaydata", "agents", "claudecode", "scenarios", "1-5_not-runnable", desktopResultsFile), "")
	// Remove the empty placeholder entirely: an absent result file is allowed
	// in draft mode, while a present malformed file must still fail.
	if err := os.Remove(filepath.Join(fixture.root, "replaydata", "agents", "claudecode", "scenarios", "1-5_not-runnable", desktopResultsFile)); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := runOf("validate", "--repo-root", fixture.root); code != exitOK {
		t.Fatalf("partial Desktop results before enablement failed: exit=%d stderr=%s", code, stderr)
	}
}

// TestValidateDesktopCompletenessMutations is committed mutation evidence for
// the dynamic exact-set gate. Each mutation starts from the valid five-outcome
// fixture, breaks one set property, and requires the exact scenario identity.
func TestValidateDesktopCompletenessMutations(t *testing.T) {
	t.Run("missing current cell", func(t *testing.T) {
		fixture := desktopResultsRepo(t, true)
		path := filepath.Join(fixture.cellDirs["unobservable"], desktopResultsFile)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		requireDesktopFinding(t, fixture.root, "unobservable", "missing desktop-local result")
	})
	t.Run("Claude Code cell addition is required dynamically", func(t *testing.T) {
		fixture := desktopResultsRepo(t, true)
		path := filepath.Join(fixture.root, "replaydata", "agents", "scenarios.json")
		var catalog map[string]any
		mustReadJSON(t, path, &catalog)
		catalog["scenarios"] = append(catalog["scenarios"].([]any), map[string]any{
			"id": "1.6", "name": "new-current-cell", "description": "d", "process": "p", "acceptance_criteria": "a",
		})
		writeJSONFixture(t, path, catalog)
		cell(t, fixture.root, "claudecode", "1-6_new-current-cell", "yes", "full", "ready")
		requireDesktopFinding(t, fixture.root, "new-current-cell", "missing desktop-local result")
	})
	t.Run("other adapter scenario does not create a Claude Code requirement", func(t *testing.T) {
		fixture := desktopResultsRepo(t, true)
		path := filepath.Join(fixture.root, "replaydata", "agents", "scenarios.json")
		var catalog map[string]any
		mustReadJSON(t, path, &catalog)
		catalog["scenarios"] = append(catalog["scenarios"].([]any), map[string]any{
			"id": "1.6", "name": "other-adapter-only", "description": "d", "process": "p", "acceptance_criteria": "a",
		})
		writeJSONFixture(t, path, catalog)
		cell(t, fixture.root, "aider", "1-6_other-adapter-only", "yes", "full", "ready")
		if code, _, stderr := runOf("validate", "--repo-root", fixture.root); code != exitOK {
			t.Fatalf("other-adapter-only scenario created a Claude Desktop requirement: exit=%d stderr=%s", code, stderr)
		}
	})
	t.Run("duplicate", func(t *testing.T) {
		fixture := desktopResultsRepo(t, true)
		path := filepath.Join(fixture.cellDirs["not-applicable"], desktopResultsFile)
		var doc map[string]any
		mustReadJSON(t, path, &doc)
		results := doc["results"].([]any)
		doc["results"] = append(results, results[0])
		writeJSONFixture(t, path, doc)
		requireDesktopFinding(t, fixture.root, "not-applicable", "duplicate desktop-local result")
	})
	t.Run("duplicate across cell folders before enablement", func(t *testing.T) {
		fixture := desktopResultsRepo(t, false)
		original := fixture.cellDirs["not-applicable"]
		variant := filepath.Join(fixture.root, "replaydata", "agents", "claudecode", "scenarios", "1-3_not-applicable-variant")
		body, err := os.ReadFile(filepath.Join(original, desktopResultsFile))
		if err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(variant, "metadata.json"), `{"scenario_id":"not-applicable"}`)
		write(t, filepath.Join(variant, desktopResultsFile), string(body))
		requireDesktopFinding(t, fixture.root, "not-applicable", "duplicate desktop-local result")
	})
	t.Run("off-path result cannot satisfy a canonical cell", func(t *testing.T) {
		fixture := desktopResultsRepo(t, true)
		canonical := filepath.Join(fixture.cellDirs["unobservable"], desktopResultsFile)
		body, err := os.ReadFile(canonical)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(canonical); err != nil {
			t.Fatal(err)
		}
		offPath := filepath.Join(fixture.root, "replaydata", "agents", "claudecode", "regressions", "copied-result")
		write(t, filepath.Join(offPath, "metadata.json"), `{"scenario_id":"unobservable"}`+"\n")
		write(t, filepath.Join(offPath, desktopResultsFile), string(body))
		requireDesktopFinding(t, fixture.root, "unobservable", "missing desktop-local result", "document location")
	})
}

func TestValidateDesktopResultShapeMutations(t *testing.T) {
	cases := []struct {
		name   string
		field  string
		mutate func(map[string]any)
	}{
		{"unknown profile", "execution_profile", func(r map[string]any) { r["execution_profile"] = "remote" }},
		{"unknown outcome", "outcome", func(r map[string]any) { r["outcome"] = "maybe" }},
		{"scenario mismatch", "scenario_id", func(r map[string]any) { r["scenario_id"] = "other" }},
		{"blank reason", "reason", func(r map[string]any) { r["reason"] = "  " }},
		{"missing evidence refs", "evidence_refs", func(r map[string]any) { delete(r, "evidence_refs") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := desktopResultsRepo(t, false)
			path := filepath.Join(fixture.cellDirs["unobservable"], desktopResultsFile)
			mutateFirstResult(t, path, tc.mutate)
			requireDesktopFinding(t, fixture.root, "unobservable", tc.field)
		})
	}
	t.Run("not runnable needs the missing control", func(t *testing.T) {
		fixture := desktopResultsRepo(t, false)
		path := filepath.Join(fixture.cellDirs["not-runnable"], desktopResultsFile)
		mutateFirstResult(t, path, func(r map[string]any) { delete(r, "missing_control") })
		requireDesktopFinding(t, fixture.root, "not-runnable", "missing_control")
	})
	t.Run("unknown top-level field", func(t *testing.T) {
		fixture := desktopResultsRepo(t, false)
		path := filepath.Join(fixture.cellDirs["not-applicable"], desktopResultsFile)
		var doc map[string]any
		mustReadJSON(t, path, &doc)
		doc["surprise"] = true
		writeJSONFixture(t, path, doc)
		requireDesktopFinding(t, fixture.root, "not-applicable", "unknown field")
	})
}

func TestValidateDesktopCompletenessSwitchMutations(t *testing.T) {
	cases := []struct {
		name  string
		value any
	}{
		{"null", nil},
		{"empty object", map[string]any{}},
		{"empty profile list", map[string]any{"claudecode": []any{}}},
		{"unknown profile", map[string]any{"claudecode": []any{"remote"}}},
		{"wrong agent", map[string]any{"codex": []any{"desktop-local"}}},
		{"duplicate profile", map[string]any{"claudecode": []any{"desktop-local", "desktop-local"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := desktopResultsRepo(t, false)
			path := filepath.Join(fixture.root, "replaydata", "agents", "scenarios.json")
			var catalog map[string]any
			mustReadJSON(t, path, &catalog)
			catalog["meta"].(map[string]any)["required_execution_profiles_by_agent"] = tc.value
			writeJSONFixture(t, path, catalog)
			requireDesktopFinding(t, fixture.root, "catalog", "required_execution_profiles_by_agent")
		})
	}
}

func TestValidateDesktopResultScanDoesNotTrustMetadata(t *testing.T) {
	t.Run("malformed sibling metadata", func(t *testing.T) {
		fixture := desktopResultsRepo(t, false)
		write(t, filepath.Join(fixture.cellDirs["unobservable"], "metadata.json"), "{")
		path := filepath.Join(fixture.cellDirs["unobservable"], desktopResultsFile)
		mutateFirstResult(t, path, func(result map[string]any) { result["outcome"] = "unknown" })
		requireDesktopFinding(t, fixture.root, "unobservable", "outcome")
	})
	t.Run("orphan result file", func(t *testing.T) {
		fixture := desktopResultsRepo(t, false)
		orphan := filepath.Join(fixture.root, "replaydata", "agents", "claudecode", "scenarios", "9-9_orphan")
		write(t, filepath.Join(orphan, desktopResultsFile), `{"schema_version":1,"results":[{"scenario_id":"ghost","execution_profile":"desktop-local","outcome":"maybe"}]}`)
		requireDesktopFinding(t, fixture.root, "ghost", "outcome")
	})
}

// TestValidateDesktopIdentityMutations is the committed false-identity corpus.
// Each row changes raw evidence while the manifest and result summary stay
// correct. A validator that trusts those summaries will accept every row.
func TestValidateDesktopIdentityMutations(t *testing.T) {
	cases := []struct {
		name  string
		field string
		file  string
		body  string
	}{
		{"CLI transcript entrypoint", "transcript.entrypoint", "transcript.jsonl", `{"type":"user","sessionId":"transcript-1","cwd":"/workspace","entrypoint":"cli"}` + "\n"},
		{"registry mapping", "desktop_registry.cliSessionId", "desktop-registry.json", `{"sessionId":"local_desktop-1","cliSessionId":"other","cwd":"/workspace"}` + "\n"},
		{"registry Desktop ID", "desktop_registry.sessionId", "desktop-registry.json", `{"sessionId":"remote_desktop-1","cliSessionId":"transcript-1","cwd":"/workspace"}` + "\n"},
		{"registry workspace", "desktop_registry.cwd", "desktop-registry.json", `{"sessionId":"local_desktop-1","cliSessionId":"transcript-1","cwd":"/other"}` + "\n"},
		{"built-in Local registry scope", "desktop_registry.envScopeId", "desktop-registry.json", `{"sessionId":"local_desktop-1","cliSessionId":"transcript-1","cwd":"/workspace","envScopeId":"builtin_local"}` + "\n"},
		{"transcript workspace", "transcript.cwd", "transcript.jsonl", `{"type":"user","sessionId":"transcript-1","cwd":"/other","entrypoint":"claude-desktop"}` + "\n"},
		{"selected environment", "environment.selected_environment", "desktop-environment.json", `{"selected_environment":"Remote","requested_workspace":"/workspace"}` + "\n"},
		{"requested workspace", "environment.requested_workspace", "desktop-environment.json", `{"selected_environment":"Local","requested_workspace":"/other"}` + "\n"},
		{"Irrlicht bundle", "irrlicht_session.launcher.host_bundle_id", "irrlicht-session.json", `{"session_id":"transcript-1","cwd":"/workspace","pid":4242,"launcher":{"host_bundle_id":"com.apple.Terminal"}}` + "\n"},
		{"Irrlicht session", "irrlicht_session.session_id", "irrlicht-session.json", `{"session_id":"other","cwd":"/workspace","pid":4242,"launcher":{"host_bundle_id":"com.anthropic.claudefordesktop"}}` + "\n"},
		{"Irrlicht workspace", "irrlicht_session.cwd", "irrlicht-session.json", `{"session_id":"transcript-1","cwd":"/other","pid":4242,"launcher":{"host_bundle_id":"com.anthropic.claudefordesktop"}}` + "\n"},
		{"hook session", "hooks.session_id", "hooks.jsonl", `{"kind":"hook_received","session_id":"other","hook_name":"Stop"}` + "\n"},
		{"process PID", "process.pid", "process.json", `{"pid":9999,"command":"claude"}` + "\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := desktopResultsRepo(t, false)
			recDir := filepath.Join(fixture.cellDirs["observed-pass"], "recordings", fixture.record)
			write(t, filepath.Join(recDir, tc.file), tc.body)
			requireDesktopFinding(t, fixture.root, "observed-pass", tc.field)
		})
	}
	t.Run("mixed hook sessions", func(t *testing.T) {
		fixture := desktopResultsRepo(t, false)
		recDir := filepath.Join(fixture.cellDirs["observed-pass"], "recordings", fixture.record)
		write(t, filepath.Join(recDir, "hooks.jsonl"), `{"kind":"hook_received","session_id":"transcript-1","hook_name":"Stop"}`+"\n"+`{"kind":"hook_received","session_id":"other","hook_name":"Stop"}`+"\n")
		requireDesktopFinding(t, fixture.root, "observed-pass", "hooks.session_id")
	})
	t.Run("session-only row is not a hook receipt", func(t *testing.T) {
		fixture := desktopResultsRepo(t, false)
		recDir := filepath.Join(fixture.cellDirs["observed-pass"], "recordings", fixture.record)
		write(t, filepath.Join(recDir, "hooks.jsonl"), `{"session_id":"transcript-1"}`+"\n")
		requireDesktopFinding(t, fixture.root, "observed-pass", "hooks.kind")
	})
	t.Run("hook name is required", func(t *testing.T) {
		fixture := desktopResultsRepo(t, false)
		recDir := filepath.Join(fixture.cellDirs["observed-pass"], "recordings", fixture.record)
		write(t, filepath.Join(recDir, "hooks.jsonl"), `{"kind":"hook_received","session_id":"transcript-1"}`+"\n")
		requireDesktopFinding(t, fixture.root, "observed-pass", "hooks.hook_name")
	})
}

func TestValidateDesktopHookReceiptProvenanceMutations(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"changed hook name", strings.Replace(desktopHookReceipt, `"hook_name":"Stop"`, `"hook_name":"PostToolUse"`, 1) + "\n"},
		{"changed session", strings.Replace(desktopHookReceipt, `"session_id":"transcript-1"`, `"session_id":"other"`, 1) + "\n"},
		{"invented extra field", strings.TrimSuffix(desktopHookReceipt, "}") + `,"source":"invented"}` + "\n"},
		{"invented duplicate", desktopHookReceipt + "\n" + desktopHookReceipt + "\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := desktopResultsRepo(t, false)
			recDir := filepath.Join(fixture.cellDirs["observed-pass"], "recordings", fixture.record)
			write(t, filepath.Join(recDir, "hooks.jsonl"), tc.body)
			requireDesktopFinding(t, fixture.root, "observed-pass", "hooks.events_jsonl")
		})
	}
}

func TestValidateDesktopObservedOutcomeMutations(t *testing.T) {
	t.Run("passing recording cannot be labelled failure", func(t *testing.T) {
		fixture := desktopResultsRepo(t, false)
		path := filepath.Join(fixture.cellDirs["observed-pass"], desktopResultsFile)
		mutateFirstResult(t, path, func(result map[string]any) {
			result["outcome"] = "observed-failure"
			result["reason"] = "Synthetic failure label."
		})
		requireDesktopFinding(t, fixture.root, "observed-pass", "outcome")
	})
	t.Run("failing recording cannot be labelled passing", func(t *testing.T) {
		fixture := desktopResultsRepo(t, false)
		path := filepath.Join(fixture.cellDirs["observed-failure"], desktopResultsFile)
		mutateFirstResult(t, path, func(result map[string]any) { result["outcome"] = "observed-passing" })
		requireDesktopFinding(t, fixture.root, "observed-failure", "outcome")
	})
	t.Run("expected report is required", func(t *testing.T) {
		fixture := desktopResultsRepo(t, false)
		if err := os.Remove(filepath.Join(fixture.cellDirs["observed-pass"], "expected.jsonl")); err != nil {
			t.Fatal(err)
		}
		requireDesktopFinding(t, fixture.root, "observed-pass", "outcome")
	})
}

func TestValidateDesktopExactRecordingCompletenessMutation(t *testing.T) {
	fixture := desktopResultsRepo(t, false)
	cellDir := fixture.cellDirs["observed-pass"]
	oldRecording := filepath.Join(cellDir, "recordings", fixture.record)
	if err := os.Remove(filepath.Join(oldRecording, "events.jsonl")); err != nil {
		t.Fatal(err)
	}
	newer := fixture
	newer.record = "desktop-r2"
	writeObservedDesktopResult(t, newer, observedResultFixture{scenario: "observed-pass", outcome: "observed-passing"})
	path := filepath.Join(cellDir, desktopResultsFile)
	mutateFirstResult(t, path, func(result map[string]any) { result["recording"] = fixture.record })
	requireDesktopFinding(t, fixture.root, "observed-pass", "recording completeness")
}

func TestValidateDesktopEvidenceReferenceMutations(t *testing.T) {
	for _, field := range []string{"desktop_registry", "transcript", "hooks", "process", "irrlicht_session", "environment"} {
		t.Run(field, func(t *testing.T) {
			fixture := desktopResultsRepo(t, false)
			path := filepath.Join(fixture.cellDirs["observed-pass"], desktopResultsFile)
			mutateFirstResult(t, path, func(result map[string]any) {
				result["evidence"].(map[string]any)[field] = "missing.json"
			})
			requireDesktopFinding(t, fixture.root, "observed-pass", "evidence."+field)
		})
	}
	t.Run("recording profile", func(t *testing.T) {
		fixture := desktopResultsRepo(t, false)
		manifest := filepath.Join(fixture.cellDirs["observed-pass"], "recordings", fixture.record, "manifest.json")
		write(t, manifest, `{"execution_profile":"cli-local","entrypoint":"claude-desktop"}`+"\n")
		requireDesktopFinding(t, fixture.root, "observed-pass", "manifest.execution_profile")
	})
	t.Run("recording traversal", func(t *testing.T) {
		fixture := desktopResultsRepo(t, false)
		path := filepath.Join(fixture.cellDirs["observed-pass"], desktopResultsFile)
		mutateFirstResult(t, path, func(result map[string]any) { result["recording"] = "../desktop-r1" })
		requireDesktopFinding(t, fixture.root, "observed-pass", "recording")
	})
	t.Run("evidence traversal", func(t *testing.T) {
		fixture := desktopResultsRepo(t, false)
		path := filepath.Join(fixture.cellDirs["observed-pass"], desktopResultsFile)
		mutateFirstResult(t, path, func(result map[string]any) {
			result["evidence"].(map[string]any)["hooks"] = "nested/../hooks.jsonl"
		})
		requireDesktopFinding(t, fixture.root, "observed-pass", "evidence.hooks")
	})
	t.Run("symlink escape", func(t *testing.T) {
		fixture := desktopResultsRepo(t, false)
		outside := filepath.Join(fixture.root, "outside-hooks.jsonl")
		write(t, outside, `{"session_id":"transcript-1"}`+"\n")
		recordingDir := filepath.Join(fixture.cellDirs["observed-pass"], "recordings", fixture.record)
		if err := os.Remove(filepath.Join(recordingDir, "hooks.jsonl")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(recordingDir, "hooks.jsonl")); err != nil {
			t.Fatal(err)
		}
		requireDesktopFinding(t, fixture.root, "observed-pass", "evidence.hooks")
	})
	t.Run("manifest entrypoint", func(t *testing.T) {
		fixture := desktopResultsRepo(t, false)
		manifest := filepath.Join(fixture.cellDirs["observed-pass"], "recordings", fixture.record, "manifest.json")
		write(t, manifest, `{"execution_profile":"desktop-local","entrypoint":"cli","daemon_version":"0.6.2","agent_cli_version":"2.1.258","desktop_app_version":"1.44121.4"}`+"\n")
		requireDesktopFinding(t, fixture.root, "observed-pass", "manifest.entrypoint")
	})
	t.Run("non-observed circular reference", func(t *testing.T) {
		fixture := desktopResultsRepo(t, false)
		path := filepath.Join(fixture.cellDirs["unobservable"], desktopResultsFile)
		mutateFirstResult(t, path, func(result map[string]any) {
			result["evidence_refs"] = []any{filepath.ToSlash(strings.TrimPrefix(path, fixture.root+string(filepath.Separator)))}
		})
		requireDesktopFinding(t, fixture.root, "unobservable", "evidence_refs[0]")
	})
	t.Run("non-observed reference outside allowed evidence scope", func(t *testing.T) {
		fixture := desktopResultsRepo(t, false)
		write(t, filepath.Join(fixture.root, "unrelated.txt"), "not campaign evidence\n")
		path := filepath.Join(fixture.cellDirs["unobservable"], desktopResultsFile)
		mutateFirstResult(t, path, func(result map[string]any) { result["evidence_refs"] = []any{"unrelated.txt"} })
		requireDesktopFinding(t, fixture.root, "unobservable", "evidence_refs[0]")
	})
	t.Run("unrelated repository doc is not Desktop evidence", func(t *testing.T) {
		fixture := desktopResultsRepo(t, false)
		write(t, filepath.Join(fixture.root, "docs", "testing-philosophy.md"), "general test guidance\n")
		path := filepath.Join(fixture.cellDirs["unobservable"], desktopResultsFile)
		mutateFirstResult(t, path, func(result map[string]any) {
			result["evidence_refs"] = []any{"docs/testing-philosophy.md"}
		})
		requireDesktopFinding(t, fixture.root, "unobservable", "evidence_refs[0]")
	})
}

func mutateFirstResult(t *testing.T, path string, mutate func(map[string]any)) {
	t.Helper()
	var doc map[string]any
	mustReadJSON(t, path, &doc)
	mutate(doc["results"].([]any)[0].(map[string]any))
	writeJSONFixture(t, path, doc)
}

func mustReadJSON(t *testing.T, path string, out any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatal(err)
	}
}

func writeJSONFixture(t *testing.T, path string, value any) {
	t.Helper()
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	write(t, path, string(b)+"\n")
}
