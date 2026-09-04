package matrix

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRecordingManifestLegacyDefaultsToCLILocal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, []byte(`{
  "daemon_version": "0.6.2+abc1234",
  "agent_cli_version": "2.1.143"
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadRecordingManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.ExecutionProfile != ProfileCLILocal {
		t.Fatalf("profile = %q; want %q", got.ExecutionProfile, ProfileCLILocal)
	}
	if got.DaemonVersion != "0.6.2+abc1234" || got.AgentCLIVersion != "2.1.143" {
		t.Fatalf("versions changed while loading legacy manifest: %+v", got)
	}
}

func mixedProfileMatrixRepo(t *testing.T) string {
	t.Helper()
	root := writeShardFixture(t, "claudecode", map[string]shardFix{"basic-turn": {}})
	cellDir := filepath.Join(root, "replaydata", "agents", "claudecode", "scenarios", "1-1_basic-turn")
	writeManifest := func(name, body string) {
		dir := filepath.Join(cellDir, "recordings", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeManifest("r1", `{"execution_profile":"cli-local","entrypoint":"cli","daemon_version":"0.6.2+cli","agent_cli_version":"2.1.143"}`)
	writeManifest("r2", `{"execution_profile":"desktop-local","entrypoint":"sdk-cli","daemon_version":"0.6.2+desktop","agent_cli_version":"2.1.143","desktop_app_version":"1.0.10"}`)
	return root
}

func TestMatrixDefaultsToCLILocalBeforeSelectingNewestRecording(t *testing.T) {
	root := mixedProfileMatrixRepo(t)
	cli, err := LoadRepo(root)
	if err != nil {
		t.Fatal(err)
	}
	cliCell, ok := cli.Cell("claudecode", "basic-turn")
	if !ok {
		t.Fatal("default CLI cell is missing")
	}
	if !cliCell.Recorded {
		t.Fatal("default CLI cell is not recorded")
	}
	if cliCell.RecordingName != "r1" {
		t.Fatalf("default selected recording %q", cliCell.RecordingName)
	}
	if cliCell.ExecutionProfile != ProfileCLILocal {
		t.Fatalf("default selected profile %q", cliCell.ExecutionProfile)
	}
	if cliCell.Entrypoint != "cli" {
		t.Fatalf("default changed the transcript entrypoint: %+v", cliCell)
	}
}

func TestMatrixSelectsDesktopLocalBeforeNewestRecording(t *testing.T) {
	root := mixedProfileMatrixRepo(t)
	desktop, err := LoadRepoForProfile(root, ProfileDesktopLocal)
	if err != nil {
		t.Fatal(err)
	}
	desktopCell, ok := desktop.Cell("claudecode", "basic-turn")
	if !ok {
		t.Fatal("Desktop cell is missing")
	}
	if !desktopCell.Recorded {
		t.Fatal("Desktop cell is not recorded")
	}
	if desktopCell.RecordingName != "r2" {
		t.Fatalf("Desktop selected recording %q", desktopCell.RecordingName)
	}
	if desktopCell.ExecutionProfile != ProfileDesktopLocal {
		t.Fatalf("Desktop selected profile %q", desktopCell.ExecutionProfile)
	}
	if desktopCell.Entrypoint != "sdk-cli" {
		t.Fatalf("Desktop changed entrypoint to %q", desktopCell.Entrypoint)
	}
	if desktopCell.DesktopAppVersion != "1.0.10" {
		t.Fatalf("Desktop app version=%q", desktopCell.DesktopAppVersion)
	}
}

func TestLoadRecordingManifestRejectsUnknownProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, []byte(`{"execution_profile":"remote"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadRecordingManifest(path)
	if err == nil || !strings.Contains(err.Error(), `unknown execution profile "remote"`) {
		t.Fatalf("error = %v; want unknown profile and value", err)
	}
}

func TestLoadRecordingManifestRejectsPresentEmptyOrNullProfile(t *testing.T) {
	for _, body := range []string{
		`{"execution_profile":""}`,
		`{"execution_profile":null}`,
	} {
		t.Run(body, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "manifest.json")
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadRecordingManifest(path); err == nil {
				t.Fatalf("LoadRecordingManifest(%s) succeeded; present empty/null profile must fail", body)
			}
		})
	}
}

func TestParseExecutionProfileRejectsEmptyInput(t *testing.T) {
	if _, err := ParseExecutionProfile(""); err == nil {
		t.Fatal("empty explicit profile must fail")
	}
}
