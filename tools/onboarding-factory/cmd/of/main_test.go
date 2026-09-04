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
	// Shares fixture_test.go's cell/recording helpers so "what a complete
	// recording is" lives in one place — validate.RecordingComplete gaining a
	// required file must not mean editing two fixtures in this package.
	cell(t, root, "claudecode", "1-1_session-start", "yes", "full", "ready")
	recording(t, root, "claudecode", "1-1_session-start", "r1", false)
	return root
}

// mixedProfileRepo keeps the flat recordings layout and makes the Desktop
// recording lexicographically newer. The default query must still select r1.
func mixedProfileRepo(t *testing.T) string {
	t.Helper()
	root := validRepo(t)
	base := filepath.Join(root, "replaydata", "agents", "claudecode", "scenarios", "1-1_session-start", "recordings")
	write(t, filepath.Join(base, "r1", "manifest.json"), `{
  "execution_profile": "cli-local",
  "entrypoint": "cli",
  "daemon_version": "0.6.2+cli",
  "agent_cli_version": "2.1.143"
}`+"\n")
	recording(t, root, "claudecode", "1-1_session-start", "r2", false)
	write(t, filepath.Join(base, "r2", "manifest.json"), `{
  "execution_profile": "desktop-local",
  "entrypoint": "sdk-cli",
  "daemon_version": "0.6.2+desktop",
  "agent_cli_version": "2.1.143",
  "desktop_app_version": "1.0.10"
}`+"\n")
	return root
}

func runOf(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := run(args, &out, &errb)
	return code, out.String(), errb.String()
}

func requireUsageError(t *testing.T, code int, text, wantText string) {
	t.Helper()
	if code != exitUsage {
		t.Fatalf("exit=%d; want %d; output=%q", code, exitUsage, text)
	}
	if !strings.Contains(text, wantText) {
		t.Fatalf("output=%q; want %q", text, wantText)
	}
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

func profileStatusView(t *testing.T, root string, profileArgs ...string) statusView {
	t.Helper()
	args := []string{"status", "--agent", "claudecode", "--scenario", "session-start"}
	args = append(args, profileArgs...)
	args = append(args, "--json", "--repo-root", root)
	code, out, errs := runOf(args...)
	if code != exitOK {
		t.Fatalf("status: exit=%d stderr=%s", code, errs)
	}
	var view statusView
	if err := json.Unmarshal([]byte(out), &view); err != nil {
		t.Fatal(err)
	}
	return view
}

func TestStatusDefaultsToCLILocalProfile(t *testing.T) {
	root := mixedProfileRepo(t)
	cli := profileStatusView(t, root)
	cliCell := cli.Scenarios[0].Cells["claudecode"]
	if cli.ExecutionProfile != "cli-local" {
		t.Fatalf("default view selected profile %q", cli.ExecutionProfile)
	}
	if cliCell.ExecutionProfile != "cli-local" {
		t.Fatalf("default cell selected profile %q", cliCell.ExecutionProfile)
	}
	if cliCell.RecordingName != "r1" {
		t.Fatalf("default cell selected recording %q", cliCell.RecordingName)
	}
	if cliCell.Entrypoint != "cli" {
		t.Fatalf("default cell changed entrypoint to %q", cliCell.Entrypoint)
	}
}

func TestStatusSelectsDesktopLocalProfile(t *testing.T) {
	root := mixedProfileRepo(t)
	desktop := profileStatusView(t, root, "--profile", "desktop-local")
	desktopCell := desktop.Scenarios[0].Cells["claudecode"]
	if desktop.ExecutionProfile != "desktop-local" {
		t.Fatalf("Desktop view selected profile %q", desktop.ExecutionProfile)
	}
	if desktopCell.ExecutionProfile != "desktop-local" {
		t.Fatalf("Desktop cell selected profile %q", desktopCell.ExecutionProfile)
	}
	if desktopCell.RecordingName != "r2" {
		t.Fatalf("Desktop cell selected recording %q", desktopCell.RecordingName)
	}
	if desktopCell.Entrypoint != "sdk-cli" {
		t.Fatalf("Desktop cell changed entrypoint to %q", desktopCell.Entrypoint)
	}
	if desktopCell.DesktopAppVersion != "1.0.10" {
		t.Fatalf("Desktop app version=%q", desktopCell.DesktopAppVersion)
	}
}

func TestStatusRejectsUnknownExecutionProfile(t *testing.T) {
	code, _, errs := runOf("status", "--profile", "remote", "--repo-root", validRepo(t))
	requireUsageError(t, code, errs, `unknown execution profile "remote"`)
}

func TestStatusRejectsEmptyExecutionProfile(t *testing.T) {
	code, _, errs := runOf("status", "--profile", "", "--repo-root", validRepo(t))
	requireUsageError(t, code, errs, `unknown execution profile ""`)
}

func TestStatusDoesNotRepeatMalformedFlagError(t *testing.T) {
	code, _, repeated := runOf("status", "--definitely-invalid")
	if code != exitUsage {
		t.Fatalf("exit=%d; want %d", code, exitUsage)
	}
	if repeated != "" {
		t.Fatalf("FlagSet already printed the error; command repeated %q", repeated)
	}
}

func TestVerifyDoesNotRepeatMalformedFlagError(t *testing.T) {
	code, _, repeated := runOf("verify", "--definitely-invalid")
	if code != exitUsage {
		t.Fatalf("exit=%d; want %d", code, exitUsage)
	}
	if repeated != "" {
		t.Fatalf("FlagSet already printed the error; command repeated %q", repeated)
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

func TestStatusRunsRejectsExplicitExecutionProfile(t *testing.T) {
	root := validRepo(t)
	for _, profile := range []string{"cli-local", "desktop-local"} {
		t.Run(profile, func(t *testing.T) {
			code, _, errs := runOf("status", "--runs", "--profile", profile, "--repo-root", root)
			requireUsageError(t, code, errs, "--profile cannot be used with --runs")
		})
	}
	code, _, errs := runOf("status", "--runs", "--profile", "remote", "--repo-root", root)
	requireUsageError(t, code, errs, `unknown execution profile "remote"`)
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
				`{}`)
		}, "missing scenario_id"},
		{"dangling scenario_id", func(r string) {
			write(t, filepath.Join(r, "replaydata", "agents", "claudecode", "scenarios", "1-1_session-start", "metadata.json"),
				`{"scenario_id":"ghost"}`)
		}, "not in the catalog"},
		{"recorded without expected", func(r string) {
			_ = os.Remove(filepath.Join(r, "replaydata", "agents", "claudecode", "scenarios", "1-1_session-start", "expected.jsonl"))
		}, "missing expected.jsonl"},
		{"orphan recording folder", func(r string) {
			write(t, filepath.Join(r, "replaydata", "agents", "claudecode", "scenarios", "9-9_orphan", "recordings", "r1", "events.jsonl"), "\n")
		}, "orphan recording folder"},
		{"incomplete newest recording (jsonl, no golden)", func(r string) {
			// Remove the newest recording's golden — a jsonl transcript without
			// its replay golden is an incomplete recording.
			_ = os.Remove(filepath.Join(r, "replaydata", "agents", "claudecode",
				"scenarios", "1-1_session-start", "recordings", "r1", "transcript.jsonl.replay.json.golden"))
		}, "incomplete recording: missing transcript.jsonl.replay.json.golden"},
		{"script recipe without timeout_seconds", func(r string) {
			// A recipe that would be driven (non-empty script, not record_blocked)
			// must carry a timeout_seconds — its absence crashed a driver.
			write(t, filepath.Join(r, "replaydata", "agents", "claudecode", "scenarios", "1-1_session-start", "metadata.json"),
				`{"scenario_id":"session-start","details":{"recipe":{"script":[{"type":"send","text":"hi"}]}}}`)
		}, "omits timeout_seconds"},
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

func TestValidateScansOlderManifestsForUnknownProfiles(t *testing.T) {
	root := validRepo(t)
	recording(t, root, "claudecode", "1-1_session-start", "r0", false)
	manifest := filepath.Join(root, "replaydata", "agents", "claudecode", "scenarios",
		"1-1_session-start", "recordings", "r0", "manifest.json")
	write(t, manifest, `{"execution_profile":"remote"}`+"\n")

	code, _, errs := runOf("validate", "--repo-root", root)
	if code != exitFail {
		t.Fatalf("exit=%d; want validation failure", code)
	}
	if !strings.Contains(errs, "recordings/r0") || !strings.Contains(errs, `unknown execution profile "remote"`) {
		t.Fatalf("finding must name the recording and profile value:\n%s", errs)
	}
}

func TestValidateScansProfilesOutsideValidScenarioCells(t *testing.T) {
	for _, tc := range []struct {
		name string
		path func(string) string
	}{
		{
			name: "regression recording",
			path: func(root string) string {
				return filepath.Join(root, "replaydata", "agents", "codex", "regressions", "issue-1", "recordings", "r1", "manifest.json")
			},
		},
		{
			name: "orphan scenario recording",
			path: func(root string) string {
				return filepath.Join(root, "replaydata", "agents", "claudecode", "scenarios", "9-9_orphan", "recordings", "r1", "manifest.json")
			},
		},
		{
			name: "malformed metadata recording",
			path: func(root string) string {
				cell := filepath.Join(root, "replaydata", "agents", "claudecode", "scenarios", "2-1_basic-turn")
				write(t, filepath.Join(cell, "metadata.json"), "{")
				return filepath.Join(cell, "recordings", "r1", "manifest.json")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := validRepo(t)
			manifest := tc.path(root)
			write(t, manifest, `{"execution_profile":"remote"}`+"\n")

			code, _, errs := runOf("validate", "--repo-root", root)
			if code != exitFail {
				t.Fatalf("exit=%d stderr=%q; want validation failure", code, errs)
			}
			wantPath := filepath.ToSlash(strings.TrimPrefix(filepath.Dir(manifest), root+string(filepath.Separator)))
			if !strings.Contains(errs, wantPath) {
				t.Fatalf("stderr=%q; want recording path %q", errs, wantPath)
			}
			if !strings.Contains(errs, `unknown execution profile "remote"`) {
				t.Fatalf("stderr=%q; want unknown profile value", errs)
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
