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

func TestMatrixSelectsProfileBeforeNewestRecording(t *testing.T) {
	root := writeShardFixture(t, "claudecode", map[string]shardFix{"basic-turn": {}})
	cellDir := filepath.Join(root, "replaydata", "agents", "claudecode", "scenarios", "1-1_basic-turn")
	writeManifest := func(name, body string) {
		t.Helper()
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

	cli, err := LoadRepo(root)
	if err != nil {
		t.Fatal(err)
	}
	cliCell, ok := cli.Cell("claudecode", "basic-turn")
	if !ok || !cliCell.Recorded {
		t.Fatalf("default CLI cell not recorded: ok=%v cell=%+v", ok, cliCell)
	}
	if cliCell.RecordingName != "r1" || cliCell.ExecutionProfile != ProfileCLILocal || cliCell.Entrypoint != "cli" {
		t.Fatalf("default selected the wrong recording: %+v", cliCell)
	}

	desktop, err := LoadRepoForProfile(root, ProfileDesktopLocal)
	if err != nil {
		t.Fatal(err)
	}
	desktopCell, ok := desktop.Cell("claudecode", "basic-turn")
	if !ok || !desktopCell.Recorded {
		t.Fatalf("Desktop cell not recorded: ok=%v cell=%+v", ok, desktopCell)
	}
	if desktopCell.RecordingName != "r2" || desktopCell.ExecutionProfile != ProfileDesktopLocal ||
		desktopCell.Entrypoint != "sdk-cli" || desktopCell.DesktopAppVersion != "1.0.10" {
		t.Fatalf("Desktop selected the wrong recording identity: %+v", desktopCell)
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
