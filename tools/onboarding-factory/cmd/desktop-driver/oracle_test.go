package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"irrlicht/core/adapters/inbound/agents/claudecode"
	"irrlicht/core/adapters/outbound/filesystem"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/config"
	"irrlicht/core/pkg/daemonaddr"
)

type oracleLogger struct{}

func (*oracleLogger) LogInfo(string, string, string)                       {}
func (*oracleLogger) LogError(string, string, string)                      {}
func (*oracleLogger) LogProcessingTime(string, string, int64, int, string) {}
func (*oracleLogger) Close() error                                         { return nil }

func TestManagedFileOracleRunsRealClaudeCodeApplyClosuresInShadowHome(t *testing.T) {
	realHome := t.TempDir()
	t.Setenv("HOME", realHome)
	baseline := filepath.Join(t.TempDir(), "baseline")
	if err := os.Mkdir(baseline, 0o700); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(realHome, ".claude", "settings.json")
	memoryPath := filepath.Join(realHome, ".claude", "CLAUDE.md")
	settingsBaseline := []byte(`{"userKey":"keep","statusLine":{"command":"user-status"}}` + "\n")
	memoryBaseline := []byte("user prose\n")
	if err := os.WriteFile(filepath.Join(baseline, "0"), settingsBaseline, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(baseline, "1"), memoryBaseline, 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := "saved\t0\t" + settingsPath + "\n" + "saved\t1\t" + memoryPath + "\n"
	if err := os.WriteFile(filepath.Join(baseline, "manifest"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(baseline, "oracle")
	if err := buildManagedFileOracle(managedFileOracleOptions{
		baselineDir: baseline, outputDir: output, realHome: realHome, bindAddress: "127.0.0.1:41234",
	}); err != nil {
		t.Fatalf("buildManagedFileOracle() error = %v", err)
	}
	settings, err := os.ReadFile(filepath.Join(output, "0"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(settings, &decoded); err != nil {
		t.Fatal(err)
	}
	hooks, hooksOK := decoded["hooks"].(map[string]any)
	statusLine, statusOK := decoded["statusLine"].(map[string]any)
	statusCommand, commandOK := statusLine["command"].(string)
	if decoded["userKey"] != "keep" || !hooksOK || hooks["Stop"] == nil ||
		!statusOK || !commandOK || !strings.Contains(statusCommand, "user-status") ||
		!strings.Contains(statusCommand, "localhost:41234/api/v1/hooks/claudecode/statusline") {
		t.Fatalf("oracle settings do not contain baseline plus real Apply output: %s", settings)
	}
	memory, err := os.ReadFile(filepath.Join(output, "1"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(memory), "user prose") || !strings.Contains(string(memory), "irrlicht") {
		t.Fatalf("oracle memory does not contain baseline plus real Apply output: %s", memory)
	}
	if _, err := os.Stat(settingsPath); !os.IsNotExist(err) {
		t.Fatalf("oracle changed the real settings path: %v", err)
	}
	if _, err := os.Stat(memoryPath); !os.IsNotExist(err) {
		t.Fatalf("oracle changed the real memory path: %v", err)
	}
	stateEntries, err := os.ReadDir(filepath.Join(output, "states"))
	if err != nil {
		t.Fatal(err)
	}
	if len(stateEntries) != 8 {
		t.Fatalf("oracle state count = %d; want all 8 subsets", len(stateEntries))
	}
	middleSkipped, err := os.ReadFile(filepath.Join(output, "states", "5", "0"))
	if err != nil {
		t.Fatal(err)
	}
	var middleSettings map[string]any
	if err := json.Unmarshal(middleSkipped, &middleSettings); err != nil {
		t.Fatal(err)
	}
	middleStatus, ok := middleSettings["statusLine"].(map[string]any)
	if !ok || middleStatus["command"] != "user-status" || middleSettings["hooks"] == nil {
		t.Fatalf("middle-skipped oracle state did not apply hooks then instructions only: %s", middleSkipped)
	}
	middleMemory, err := os.ReadFile(filepath.Join(output, "states", "5", "1"))
	if err != nil || !strings.Contains(string(middleMemory), "irrlicht") {
		t.Fatalf("later instruction effect is absent from middle-skipped state: %s (%v)", middleMemory, err)
	}
}

func TestRunDispatchesManagedFileOracleBeforeDesktopOptions(t *testing.T) {
	err := run(context.Background(), []string{"managed-file-oracle"})
	if err == nil || !strings.Contains(err.Error(), "baseline directory must be absolute") {
		t.Fatalf("managed-file-oracle dispatch error = %v", err)
	}
}

func TestManagedFileOracleFullStateMatchesRealPermissionService(t *testing.T) {
	realHome := t.TempDir()
	t.Setenv("HOME", realHome)
	t.Setenv(daemonaddr.EnvBindAddr, "127.0.0.1:41234")
	fakeBin := t.TempDir()
	claudeBin := filepath.Join(fakeBin, "claude")
	if err := os.WriteFile(claudeBin, []byte("#!/bin/sh\nprintf '2.1.260\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin)
	baseline := filepath.Join(t.TempDir(), "baseline")
	if err := os.Mkdir(baseline, 0o700); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(realHome, ".claude", "settings.json")
	memoryPath := filepath.Join(realHome, ".claude", "CLAUDE.md")
	settingsBaseline := []byte(`{"userKey":"keep","statusLine":{"command":"user-status"}}` + "\n")
	memoryBaseline := []byte("user prose\n")
	if err := os.WriteFile(filepath.Join(baseline, "0"), settingsBaseline, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(baseline, "1"), memoryBaseline, 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := "saved\t0\t" + settingsPath + "\n" + "saved\t1\t" + memoryPath + "\n"
	if err := os.WriteFile(filepath.Join(baseline, "manifest"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(baseline, "oracle")
	if err := buildManagedFileOracle(managedFileOracleOptions{
		baselineDir: baseline, outputDir: output, realHome: realHome, bindAddress: "127.0.0.1:41234",
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Dir(settingsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, settingsBaseline, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(memoryPath, memoryBaseline, 0o600); err != nil {
		t.Fatal(err)
	}
	service := services.NewPermissionService(services.PermissionServiceDeps{
		Agents:                  []agent.Agent{claudecode.Agent()},
		Store:                   filesystem.NewPermissionStore(t.TempDir()),
		Push:                    services.NewPushService(),
		Log:                     &oracleLogger{},
		Mode:                    config.PermissionModeGrantAll,
		IsolatedHome:            t.TempDir(),
		AllowSharedConfigWrites: true,
		RecordAdapters:          []string{claudecode.AdapterName},
	})
	service.Start(context.Background())
	if got := service.Snapshot().UnappliedGrants; len(got) != 0 {
		t.Fatalf("PermissionService has unapplied grants: %+v", got)
	}
	for slot, realPath := range map[string]string{"0": settingsPath, "1": memoryPath} {
		want, err := os.ReadFile(filepath.Join(output, slot))
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(realPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Fatalf("PermissionService output for slot %s differs from oracle", slot)
		}
	}
}

func TestManagedFileOracleRejectsEscapingManifestSlot(t *testing.T) {
	realHome := t.TempDir()
	t.Setenv("HOME", realHome)
	root := t.TempDir()
	baseline := filepath.Join(root, "baseline")
	if err := os.Mkdir(baseline, 0o700); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(realHome, ".claude", "settings.json")
	memoryPath := filepath.Join(realHome, ".claude", "CLAUDE.md")
	if err := os.WriteFile(filepath.Join(root, "escaped"), []byte("baseline"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(baseline, "1"), []byte("memory"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := "saved\t../escaped\t" + settingsPath + "\n" + "saved\t1\t" + memoryPath + "\n"
	if err := os.WriteFile(filepath.Join(baseline, "manifest"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	err := buildManagedFileOracle(managedFileOracleOptions{
		baselineDir: baseline,
		outputDir:   filepath.Join(baseline, "oracle"),
		realHome:    realHome,
		bindAddress: "127.0.0.1:41234",
	})
	if err == nil || !strings.Contains(err.Error(), "slot") {
		t.Fatalf("buildManagedFileOracle() escaping slot error = %v", err)
	}
}
