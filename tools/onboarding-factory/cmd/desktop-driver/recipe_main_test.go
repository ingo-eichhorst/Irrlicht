package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"irrlicht/tools/onboarding-factory/internal/desktopdriver"
)

func stagedCell(t *testing.T) (repo string, cell string) {
	t.Helper()
	repo = t.TempDir()
	cell = filepath.Join(repo, ".build", "refresh", "cell")
	if err := os.MkdirAll(filepath.Join(cell, "cwd"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(cell, "recordings"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".build", "helper"), []byte("helper"), 0o700); err != nil {
		t.Fatal(err)
	}
	return repo, cell
}

func driveArgs(repo, cell string, input ...string) []string {
	args := []string{
		"--repo-root", repo,
		"--staging", cell,
		"--workspace", filepath.Join(cell, "cwd"),
		"--helper", filepath.Join(repo, ".build", "helper"),
		"--daemon-address", "127.0.0.1:34567",
		"--recordings", filepath.Join(cell, "recordings"),
		"--irrlicht-version", "0.7.0+test",
		"--timeout", "60s",
	}
	return append(args, input...)
}

func TestParseOptionsRequiresExactlyOneInputForm(t *testing.T) {
	repo, cell := stagedCell(t)
	prompt := filepath.Join(cell, "prompt")
	script := filepath.Join(cell, "script.json")
	if err := os.WriteFile(prompt, []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte(`[{"type":"send","text":"ok"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := parseOptions(driveArgs(repo, cell, "--script-file", script)); err != nil {
		t.Fatalf("parseOptions(--script-file) error = %v", err)
	}
	if _, err := parseOptions(driveArgs(repo, cell, "--prompt-file", prompt)); err != nil {
		t.Fatalf("parseOptions(--prompt-file) error = %v", err)
	}
	for _, args := range [][]string{
		driveArgs(repo, cell),
		driveArgs(repo, cell, "--prompt-file", prompt, "--script-file", script),
	} {
		_, err := parseOptions(args)
		if err == nil || !strings.Contains(err.Error(), "exactly one of --prompt-file and --script-file") {
			t.Fatalf("parseOptions() error = %v; want the two-form refusal", err)
		}
	}
}

func TestParseOptionsConfinesTheScriptFileToStaging(t *testing.T) {
	repo, cell := stagedCell(t)
	outside := filepath.Join(t.TempDir(), "script.json")
	if err := os.WriteFile(outside, []byte(`[{"type":"send","text":"ok"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := parseOptions(driveArgs(repo, cell, "--script-file", outside))
	if err == nil || !strings.Contains(err.Error(), "script file must resolve under") {
		t.Fatalf("parseOptions() error = %v; want a confinement refusal", err)
	}
}

// The command must exit 6 for `not runnable through Desktop`, so a caller can
// tell "this recipe needs a control we do not have" — which changed nothing —
// apart from a run that started and failed.
func TestRunReturnsANotRunnableErrorForAnUndrivableRecipe(t *testing.T) {
	repo, cell := stagedCell(t)
	script := filepath.Join(cell, "script.json")
	if err := os.WriteFile(script,
		[]byte(`[{"type":"send","text":"ok"},{"type":"reset_session"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	err := run(context.Background(), driveArgs(repo, cell, "--script-file", script))
	var notRunnable *desktopdriver.NotRunnableError
	if !errors.As(err, &notRunnable) {
		t.Fatalf("run() error = %v; want *NotRunnableError (the exit-6 class)", err)
	}
	if !strings.Contains(err.Error(), "session-reset") {
		t.Fatalf("run() did not name the missing control: %v", err)
	}
}

func TestPlanSubcommandAnswersWithoutDrivingAnything(t *testing.T) {
	dir := t.TempDir()
	runnable := filepath.Join(dir, "runnable.json")
	refused := filepath.Join(dir, "refused.json")
	if err := os.WriteFile(runnable,
		[]byte(`[{"type":"send","text":"ok"},{"type":"wait_turn"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(refused, []byte(`[{"type":"slash","text":"/model opus"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"plan", "--script-file", runnable}); err != nil {
		t.Fatalf("plan(runnable) error = %v", err)
	}
	err := run(context.Background(), []string{"plan", "--script-file", refused})
	var notRunnable *desktopdriver.NotRunnableError
	if !errors.As(err, &notRunnable) {
		t.Fatalf("plan(refused) error = %v; want *NotRunnableError", err)
	}
	if err := run(context.Background(), []string{"plan"}); err == nil {
		t.Fatal("plan with no --script-file was accepted")
	}
}

func TestWriteResultFilesCarriesEverySessionIdentity(t *testing.T) {
	staging := t.TempDir()
	result := desktopdriver.RunResult{
		Owned: desktopdriver.OwnedSession{
			Registry:   desktopdriver.RegistrySession{SessionID: "local_a", CLISessionID: "cli-a"},
			Transcript: desktopdriver.TranscriptIdentity{SessionID: "cli-a"},
		},
		Sessions: []desktopdriver.SessionRecord{{
			Slot: 1, DesktopSessionID: "local_a", TranscriptID: "cli-a",
			Workspace: "/w", IrrlichtSessionID: "cli-a",
		}},
		Evidence: desktopdriver.CapturedEvidence{TranscriptPath: "/t/cli-a.jsonl"},
	}
	if err := writeResultFiles(staging, result); err != nil {
		t.Fatalf("writeResultFiles() error = %v", err)
	}
	for name, want := range map[string]string{
		"session.uuid":        "cli-a\n",
		"session.uuids":       "cli-a\n",
		"desktop.session-id":  "local_a\n",
		"desktop.session-ids": "local_a\n",
		"transcript.path":     "/t/cli-a.jsonl\n",
		"transcript.paths":    "/t/cli-a.jsonl\n",
	} {
		raw, err := os.ReadFile(filepath.Join(staging, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if string(raw) != want {
			t.Fatalf("%s = %q, want %q", name, raw, want)
		}
	}
	raw, err := os.ReadFile(filepath.Join(staging, "desktop.sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	var sessions []desktopdriver.SessionRecord
	if err := json.Unmarshal(raw, &sessions); err != nil {
		t.Fatalf("decode desktop.sessions.json: %v", err)
	}
	if len(sessions) != 1 || sessions[0].DesktopSessionID != "local_a" {
		t.Fatalf("desktop.sessions.json = %+v", sessions)
	}
}

// run-cell.sh zips session.uuids against transcript.paths. A run that owned
// more sessions than it resolved transcript paths for would curate one
// session's transcript into a fixture naming several, so it must refuse.
func TestWriteResultFilesRefusesAShortTranscriptList(t *testing.T) {
	staging := t.TempDir()
	result := desktopdriver.RunResult{
		Sessions: []desktopdriver.SessionRecord{
			{Slot: 1, DesktopSessionID: "local_a", TranscriptID: "cli-a"},
			{Slot: 2, DesktopSessionID: "local_b", TranscriptID: "cli-b"},
		},
		Evidence: desktopdriver.CapturedEvidence{TranscriptPath: "/t/cli-a.jsonl"},
	}
	err := writeResultFiles(staging, result)
	if err == nil || !strings.Contains(err.Error(), "multi-session evidence staging is not wired") {
		t.Fatalf("writeResultFiles() error = %v; want a loud refusal", err)
	}
}

// The shell wrapper's scraped declaration is generated from this subcommand.
// If the two ever disagree, the regeneration recipe in driver-desktop.sh's
// header is wrong and the drift guard has nothing trustworthy to point at.
func TestPrimitivesPrintsBothScrapedDeclarations(t *testing.T) {
	if err := printPrimitives([]string{"unexpected"}); err == nil {
		t.Fatal("primitives accepted an argument")
	}
	stdout := os.Stdout
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = write
	printErr := printPrimitives(nil)
	write.Close()
	os.Stdout = stdout
	if printErr != nil {
		t.Fatalf("printPrimitives() error = %v", printErr)
	}
	var builder strings.Builder
	buffer := make([]byte, 4096)
	for {
		read, readErr := read.Read(buffer)
		builder.Write(buffer[:read])
		if readErr != nil {
			break
		}
	}
	output := builder.String()
	for _, want := range []string{
		`DRIVE_ELICITS="` + strings.Join(desktopdriver.Primitives(), " ") + `"`,
		`DRIVE_MISSING_CONTROLS="` + strings.Join(desktopdriver.MissingControls(), " ") + `"`,
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("primitives output %q does not carry %q", output, want)
		}
	}
}
