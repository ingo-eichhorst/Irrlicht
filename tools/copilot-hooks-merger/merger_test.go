package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestMergeHooks_WritesAllEvents(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".copilot", "hooks", "irrlicht.json")

	m := NewHooksMerger(configPath)
	if err := m.MergeHooks(); err != nil {
		t.Fatalf("MergeHooks: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	var cfg copilotHooksConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if cfg.Version != 1 {
		t.Errorf("want version=1, got %d", cfg.Version)
	}
	if cfg.Disabled {
		t.Error("want disabled=false, got true")
	}

	for _, event := range copilotEvents {
		stanzas, ok := cfg.Hooks[event]
		if !ok {
			t.Errorf("missing hook stanza for event %q", event)
			continue
		}
		if len(stanzas) != 1 {
			t.Errorf("event %q: want 1 stanza, got %d", event, len(stanzas))
			continue
		}
		s := stanzas[0]
		if s.Type != "command" {
			t.Errorf("event %q: want type=command, got %q", event, s.Type)
		}
		wantBash := "irrlicht-hook-copilot --event " + event
		if s.Bash != wantBash {
			t.Errorf("event %q: want bash=%q, got %q", event, wantBash, s.Bash)
		}
		if s.TimeoutSec != 5 {
			t.Errorf("event %q: want timeoutSec=5, got %d", event, s.TimeoutSec)
		}
	}
}

func TestMergeHooks_Idempotent(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "irrlicht.json")

	m := NewHooksMerger(configPath)
	if err := m.MergeHooks(); err != nil {
		t.Fatalf("first MergeHooks: %v", err)
	}
	if err := m.MergeHooks(); err != nil {
		t.Fatalf("second MergeHooks: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var cfg copilotHooksConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	// Each event should still have exactly 1 stanza after two merges.
	for _, event := range copilotEvents {
		if len(cfg.Hooks[event]) != 1 {
			t.Errorf("event %q: want 1 stanza after idempotent merge, got %d", event, len(cfg.Hooks[event]))
		}
	}
}

func TestMergeDisable(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "irrlicht.json")

	m := NewHooksMerger(configPath)
	if err := m.MergeDisable(); err != nil {
		t.Fatalf("MergeDisable: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var cfg copilotHooksConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !cfg.Disabled {
		t.Error("want disabled=true, got false")
	}
}

func TestRemove(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "irrlicht.json")

	m := NewHooksMerger(configPath)
	if err := m.MergeHooks(); err != nil {
		t.Fatalf("MergeHooks: %v", err)
	}
	if err := m.Remove(); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Error("config file should not exist after Remove")
	}
}

func TestRemove_NoFile(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "irrlicht.json")

	m := NewHooksMerger(configPath)
	// Remove on non-existent file should not error.
	if err := m.Remove(); err != nil {
		t.Fatalf("Remove on missing file: %v", err)
	}
}

func TestCreateBackup(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "irrlicht.json")

	m := NewHooksMerger(configPath)
	if err := m.MergeHooks(); err != nil {
		t.Fatalf("MergeHooks: %v", err)
	}

	backupPath, err := m.CreateBackup()
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}
	if backupPath == "" {
		t.Fatal("expected non-empty backup path")
	}
	if _, err := os.Stat(backupPath); os.IsNotExist(err) {
		t.Errorf("backup file does not exist: %s", backupPath)
	}
}

func TestRestoreFromBackup(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "irrlicht.json")

	m := NewHooksMerger(configPath)
	if err := m.MergeHooks(); err != nil {
		t.Fatalf("MergeHooks: %v", err)
	}

	backupPath, err := m.CreateBackup()
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	// Remove config and restore.
	os.Remove(configPath)
	if err := m.RestoreFromBackup(backupPath); err != nil {
		t.Fatalf("RestoreFromBackup: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile after restore: %v", err)
	}
	var cfg copilotHooksConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("Unmarshal after restore: %v", err)
	}
	if cfg.Version != 1 {
		t.Errorf("want version=1 after restore, got %d", cfg.Version)
	}
}

func TestDryRun(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "irrlicht.json")

	m := NewHooksMerger(configPath)
	m.SetDryRun(true)
	if err := m.MergeHooks(); err != nil {
		t.Fatalf("MergeHooks (dry-run): %v", err)
	}
	// File should NOT have been created in dry-run mode.
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Error("config file should not exist in dry-run mode")
	}
}

func TestGetPreview(t *testing.T) {
	m := NewHooksMerger("/tmp/irrlicht-preview-test.json")
	preview, err := m.GetPreview()
	if err != nil {
		t.Fatalf("GetPreview: %v", err)
	}
	if preview == "" {
		t.Error("expected non-empty preview")
	}
	// Preview should be valid JSON.
	var cfg copilotHooksConfig
	if err := json.Unmarshal([]byte(preview), &cfg); err != nil {
		t.Errorf("preview is not valid JSON: %v\n%s", err, preview)
	}
}
