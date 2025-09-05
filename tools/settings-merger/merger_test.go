package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// setupTestDir creates a temporary directory for testing
func setupTestDir(t *testing.T) (string, func()) {
	tmpDir, err := os.MkdirTemp("", "irrlicht_test_*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	cleanup := func() {
		os.RemoveAll(tmpDir)
	}

	return tmpDir, cleanup
}

// createTestSettings creates a test settings file with given content
func createTestSettings(t *testing.T, dir string, content map[string]interface{}) string {
	settingsPath := filepath.Join(dir, "settings.json")
	
	data, err := json.MarshalIndent(content, "", "  ")
	if err != nil {
		t.Fatalf("Failed to marshal test settings: %v", err)
	}

	if err := os.WriteFile(settingsPath, data, 0644); err != nil {
		t.Fatalf("Failed to write test settings: %v", err)
	}

	return settingsPath
}

func TestNewSettingsMerger(t *testing.T) {
	merger := NewSettingsMerger("/test/path")
	
	if merger.settingsPath != "/test/path" {
		t.Errorf("Expected settings path '/test/path', got '%s'", merger.settingsPath)
	}
	
	if merger.dryRun {
		t.Error("Expected dry run to be false by default")
	}
	
	if merger.verbose {
		t.Error("Expected verbose to be false by default")
	}
}

func TestSetOptions(t *testing.T) {
	merger := NewSettingsMerger("/test/path")
	
	merger.SetDryRun(true)
	if !merger.dryRun {
		t.Error("Expected dry run to be true after setting")
	}
	
	merger.SetVerbose(true)
	if !merger.verbose {
		t.Error("Expected verbose to be true after setting")
	}
}

func TestLoadSettings_NonExistentFile(t *testing.T) {
	tmpDir, cleanup := setupTestDir(t)
	defer cleanup()
	
	settingsPath := filepath.Join(tmpDir, "nonexistent.json")
	merger := NewSettingsMerger(settingsPath)
	
	settings, err := merger.LoadSettings()
	if err != nil {
		t.Fatalf("Expected no error for non-existent file, got: %v", err)
	}
	
	if len(settings) != 0 {
		t.Errorf("Expected empty settings map, got %v", settings)
	}
}

func TestLoadSettings_ValidFile(t *testing.T) {
	tmpDir, cleanup := setupTestDir(t)
	defer cleanup()
	
	testContent := map[string]interface{}{
		"existing_key": "existing_value",
		"hooks": map[string]interface{}{
			"other_hook": map[string]interface{}{
				"events":  []string{"SomeEvent"},
				"command": "some-command",
			},
		},
	}
	
	settingsPath := createTestSettings(t, tmpDir, testContent)
	merger := NewSettingsMerger(settingsPath)
	
	settings, err := merger.LoadSettings()
	if err != nil {
		t.Fatalf("Failed to load settings: %v", err)
	}
	
	if settings["existing_key"] != "existing_value" {
		t.Errorf("Expected existing_key to be 'existing_value', got %v", settings["existing_key"])
	}
}

func TestLoadSettings_InvalidJSON(t *testing.T) {
	tmpDir, cleanup := setupTestDir(t)
	defer cleanup()
	
	settingsPath := filepath.Join(tmpDir, "invalid.json")
	invalidJSON := `{"key": invalid}`
	
	if err := os.WriteFile(settingsPath, []byte(invalidJSON), 0644); err != nil {
		t.Fatalf("Failed to write invalid JSON: %v", err)
	}
	
	merger := NewSettingsMerger(settingsPath)
	
	_, err := merger.LoadSettings()
	if err == nil {
		t.Error("Expected error for invalid JSON, got none")
	}
}

func TestCreateBackup(t *testing.T) {
	tmpDir, cleanup := setupTestDir(t)
	defer cleanup()
	
	testContent := map[string]interface{}{"test": "content"}
	settingsPath := createTestSettings(t, tmpDir, testContent)
	
	merger := NewSettingsMerger(settingsPath)
	
	backupPath, err := merger.CreateBackup()
	if err != nil {
		t.Fatalf("Failed to create backup: %v", err)
	}
	
	if backupPath == "" {
		t.Error("Expected non-empty backup path")
	}
	
	// Verify backup file exists and has correct content
	if _, err := os.Stat(backupPath); os.IsNotExist(err) {
		t.Errorf("Backup file does not exist: %s", backupPath)
	}
	
	// Verify backup content matches original
	backupData, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("Failed to read backup file: %v", err)
	}
	
	var backupContent map[string]interface{}
	if err := json.Unmarshal(backupData, &backupContent); err != nil {
		t.Fatalf("Failed to parse backup JSON: %v", err)
	}
	
	if backupContent["test"] != "content" {
		t.Errorf("Backup content doesn't match original")
	}
	
	// Verify metadata file exists
	metadataPath := backupPath + ".meta"
	if _, err := os.Stat(metadataPath); os.IsNotExist(err) {
		t.Errorf("Backup metadata file does not exist: %s", metadataPath)
	}
}

func TestCreateBackup_DryRun(t *testing.T) {
	tmpDir, cleanup := setupTestDir(t)
	defer cleanup()
	
	testContent := map[string]interface{}{"test": "content"}
	settingsPath := createTestSettings(t, tmpDir, testContent)
	
	merger := NewSettingsMerger(settingsPath)
	merger.SetDryRun(true)
	
	backupPath, err := merger.CreateBackup()
	if err != nil {
		t.Fatalf("Failed to create backup: %v", err)
	}
	
	// In dry run mode, backup file should not actually be created
	if _, err := os.Stat(backupPath); !os.IsNotExist(err) {
		t.Error("Expected backup file to not exist in dry run mode")
	}
}

func TestMergeIrrlichtHooks_NewFile(t *testing.T) {
	tmpDir, cleanup := setupTestDir(t)
	defer cleanup()
	
	settingsPath := filepath.Join(tmpDir, "new_settings.json")
	merger := NewSettingsMerger(settingsPath)
	
	if err := merger.MergeIrrlichtHooks(); err != nil {
		t.Fatalf("Failed to merge hooks into new file: %v", err)
	}
	
	// Verify file was created with correct content
	settings, err := merger.LoadSettings()
	if err != nil {
		t.Fatalf("Failed to load merged settings: %v", err)
	}
	
	hooks, ok := settings["hooks"].(map[string]interface{})
	if !ok {
		t.Fatal("Expected hooks section to exist")
	}
	
	irrlicht, ok := hooks["irrlicht"].(map[string]interface{})
	if !ok {
		t.Fatal("Expected irrlicht hook to exist")
	}
	
	if irrlicht["command"] != "irrlicht-hook" {
		t.Errorf("Expected command to be 'irrlicht-hook', got %v", irrlicht["command"])
	}
}

func TestMergeIrrlichtHooks_ExistingFile(t *testing.T) {
	tmpDir, cleanup := setupTestDir(t)
	defer cleanup()
	
	testContent := map[string]interface{}{
		"existing_key": "existing_value",
		"hooks": map[string]interface{}{
			"other_hook": map[string]interface{}{
				"events":  []string{"SomeEvent"},
				"command": "some-command",
			},
		},
	}
	
	settingsPath := createTestSettings(t, tmpDir, testContent)
	merger := NewSettingsMerger(settingsPath)
	
	if err := merger.MergeIrrlichtHooks(); err != nil {
		t.Fatalf("Failed to merge hooks: %v", err)
	}
	
	// Verify existing content is preserved
	settings, err := merger.LoadSettings()
	if err != nil {
		t.Fatalf("Failed to load merged settings: %v", err)
	}
	
	if settings["existing_key"] != "existing_value" {
		t.Error("Existing key was not preserved")
	}
	
	hooks := settings["hooks"].(map[string]interface{})
	
	// Verify existing hook is preserved
	if _, exists := hooks["other_hook"]; !exists {
		t.Error("Existing hook was not preserved")
	}
	
	// Verify Irrlicht hook was added
	if _, exists := hooks["irrlicht"]; !exists {
		t.Error("Irrlicht hook was not added")
	}
}

func TestMergeIrrlichtHooks_Idempotent(t *testing.T) {
	tmpDir, cleanup := setupTestDir(t)
	defer cleanup()
	
	settingsPath := filepath.Join(tmpDir, "settings.json")
	merger := NewSettingsMerger(settingsPath)
	
	// First merge
	if err := merger.MergeIrrlichtHooks(); err != nil {
		t.Fatalf("First merge failed: %v", err)
	}
	
	// Get checksum of first result
	firstData, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("Failed to read first result: %v", err)
	}
	
	// Second merge (should be idempotent)
	if err := merger.MergeIrrlichtHooks(); err != nil {
		t.Fatalf("Second merge failed: %v", err)
	}
	
	// Get checksum of second result
	secondData, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("Failed to read second result: %v", err)
	}
	
	// Results should be identical
	if string(firstData) != string(secondData) {
		t.Error("Merge operation is not idempotent")
	}
}

func TestRemoveIrrlichtHooks(t *testing.T) {
	tmpDir, cleanup := setupTestDir(t)
	defer cleanup()
	
	// Create settings with Irrlicht hook and other content
	testContent := map[string]interface{}{
		"existing_key": "existing_value",
		"hooks": map[string]interface{}{
			"irrlicht": map[string]interface{}{
				"events":  []string{"SessionStart"},
				"command": "irrlicht-hook",
			},
			"other_hook": map[string]interface{}{
				"events":  []string{"SomeEvent"},
				"command": "some-command",
			},
		},
	}
	
	settingsPath := createTestSettings(t, tmpDir, testContent)
	merger := NewSettingsMerger(settingsPath)
	
	if err := merger.RemoveIrrlichtHooks(); err != nil {
		t.Fatalf("Failed to remove hooks: %v", err)
	}
	
	// Verify Irrlicht hook was removed but other content preserved
	settings, err := merger.LoadSettings()
	if err != nil {
		t.Fatalf("Failed to load settings after removal: %v", err)
	}
	
	if settings["existing_key"] != "existing_value" {
		t.Error("Existing key was not preserved")
	}
	
	hooks := settings["hooks"].(map[string]interface{})
	
	// Verify Irrlicht hook is gone
	if _, exists := hooks["irrlicht"]; exists {
		t.Error("Irrlicht hook was not removed")
	}
	
	// Verify other hook is preserved
	if _, exists := hooks["other_hook"]; !exists {
		t.Error("Other hook was incorrectly removed")
	}
}

func TestRemoveIrrlichtHooks_EmptyHooksSection(t *testing.T) {
	tmpDir, cleanup := setupTestDir(t)
	defer cleanup()
	
	// Create settings with only Irrlicht hook
	testContent := map[string]interface{}{
		"existing_key": "existing_value",
		"hooks": map[string]interface{}{
			"irrlicht": map[string]interface{}{
				"events":  []string{"SessionStart"},
				"command": "irrlicht-hook",
			},
		},
	}
	
	settingsPath := createTestSettings(t, tmpDir, testContent)
	merger := NewSettingsMerger(settingsPath)
	
	if err := merger.RemoveIrrlichtHooks(); err != nil {
		t.Fatalf("Failed to remove hooks: %v", err)
	}
	
	settings, err := merger.LoadSettings()
	if err != nil {
		t.Fatalf("Failed to load settings after removal: %v", err)
	}
	
	// Verify hooks section was removed entirely
	if _, exists := settings["hooks"]; exists {
		t.Error("Empty hooks section was not removed")
	}
	
	// Verify other content is preserved
	if settings["existing_key"] != "existing_value" {
		t.Error("Existing key was not preserved")
	}
}

func TestRestoreFromBackup(t *testing.T) {
	tmpDir, cleanup := setupTestDir(t)
	defer cleanup()
	
	// Create original settings
	originalContent := map[string]interface{}{"original": "content"}
	settingsPath := createTestSettings(t, tmpDir, originalContent)
	
	merger := NewSettingsMerger(settingsPath)
	
	// Create backup
	backupPath, err := merger.CreateBackup()
	if err != nil {
		t.Fatalf("Failed to create backup: %v", err)
	}
	
	// Modify settings
	modifiedContent := map[string]interface{}{"modified": "content"}
	modifiedData, _ := json.MarshalIndent(modifiedContent, "", "  ")
	if err := os.WriteFile(settingsPath, modifiedData, 0644); err != nil {
		t.Fatalf("Failed to write modified settings: %v", err)
	}
	
	// Restore from backup
	if err := merger.RestoreFromBackup(backupPath); err != nil {
		t.Fatalf("Failed to restore from backup: %v", err)
	}
	
	// Verify content matches original
	settings, err := merger.LoadSettings()
	if err != nil {
		t.Fatalf("Failed to load restored settings: %v", err)
	}
	
	if settings["original"] != "content" {
		t.Error("Restored content doesn't match original")
	}
	
	if _, exists := settings["modified"]; exists {
		t.Error("Modified content still exists after restore")
	}
}

func TestListBackups(t *testing.T) {
	tmpDir, cleanup := setupTestDir(t)
	defer cleanup()
	
	testContent := map[string]interface{}{"test": "content"}
	settingsPath := createTestSettings(t, tmpDir, testContent)
	
	merger := NewSettingsMerger(settingsPath)
	
	// Create multiple backups
	backup1, _ := merger.CreateBackup()
	time.Sleep(100 * time.Millisecond) // Ensure different timestamps
	backup2, _ := merger.CreateBackup()
	
	backups, err := merger.ListBackups()
	if err != nil {
		t.Fatalf("Failed to list backups: %v", err)
	}
	
	if len(backups) != 2 {
		t.Errorf("Expected 2 backups, got %d. Backups: %v. Backup1: %s, Backup2: %s", len(backups), backups, backup1, backup2)
	}
	
	// Verify both backups are in the list
	found1, found2 := false, false
	for _, backup := range backups {
		if backup == backup1 {
			found1 = true
		}
		if backup == backup2 {
			found2 = true
		}
	}
	
	if !found1 || !found2 {
		t.Error("Not all created backups were found in list")
	}
}

func TestHooksEqual(t *testing.T) {
	merger := NewSettingsMerger("/test")
	
	hook1 := HookConfig{
		Events:  []string{"SessionStart", "Stop"},
		Command: "irrlicht-hook",
	}
	
	hook2 := HookConfig{
		Events:  []string{"Stop", "SessionStart"}, // Different order
		Command: "irrlicht-hook",
	}
	
	hook3 := HookConfig{
		Events:  []string{"SessionStart"},
		Command: "irrlicht-hook",
	}
	
	hook4 := HookConfig{
		Events:  []string{"SessionStart", "Stop"},
		Command: "different-command",
	}
	
	if !merger.hooksEqual(hook1, hook2) {
		t.Error("Expected hooks with same events in different order to be equal")
	}
	
	if merger.hooksEqual(hook1, hook3) {
		t.Error("Expected hooks with different events to be unequal")
	}
	
	if merger.hooksEqual(hook1, hook4) {
		t.Error("Expected hooks with different commands to be unequal")
	}
}