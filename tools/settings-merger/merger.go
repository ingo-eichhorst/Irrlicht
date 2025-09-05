package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// HookConfig represents a Claude Code hook configuration
type HookConfig struct {
	Events  []string `json:"events"`
	Command string   `json:"command"`
}

// BackupInfo stores metadata about a backup
type BackupInfo struct {
	OriginalPath string    `json:"original_path"`
	BackupPath   string    `json:"backup_path"`
	Timestamp    time.Time `json:"timestamp"`
	Version      string    `json:"version"`
}

// SettingsMerger handles safe merging of Claude Code settings
type SettingsMerger struct {
	settingsPath string
	dryRun       bool
	verbose      bool
}

// NewSettingsMerger creates a new settings merger
func NewSettingsMerger(settingsPath string) *SettingsMerger {
	return &SettingsMerger{
		settingsPath: settingsPath,
	}
}

// SetDryRun enables/disables dry run mode
func (sm *SettingsMerger) SetDryRun(enabled bool) {
	sm.dryRun = enabled
}

// SetVerbose enables/disables verbose logging
func (sm *SettingsMerger) SetVerbose(enabled bool) {
	sm.verbose = enabled
}

// log outputs a message if verbose mode is enabled
func (sm *SettingsMerger) log(format string, args ...interface{}) {
	if sm.verbose {
		fmt.Printf("[merger] "+format+"\n", args...)
	}
}

// LoadSettings reads and parses the Claude settings file
func (sm *SettingsMerger) LoadSettings() (map[string]interface{}, error) {
	if _, err := os.Stat(sm.settingsPath); os.IsNotExist(err) {
		sm.log("Settings file does not exist, will create new one")
		return make(map[string]interface{}), nil
	}

	data, err := os.ReadFile(sm.settingsPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read settings file: %w", err)
	}

	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, fmt.Errorf("failed to parse settings JSON: %w", err)
	}

	sm.log("Loaded existing settings with %d top-level keys", len(settings))
	return settings, nil
}

// CreateBackup creates a timestamped backup of the settings file
func (sm *SettingsMerger) CreateBackup() (string, error) {
	if _, err := os.Stat(sm.settingsPath); os.IsNotExist(err) {
		sm.log("No existing settings file to backup")
		return "", nil
	}

	timestamp := time.Now().Format("20060102_150405.000")
	backupPath := fmt.Sprintf("%s.backup_%s", sm.settingsPath, timestamp)

	if sm.dryRun {
		sm.log("DRY RUN: Would create backup at %s", backupPath)
		return backupPath, nil
	}

	// Ensure backup directory exists
	backupDir := filepath.Dir(backupPath)
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create backup directory: %w", err)
	}

	// Copy file
	src, err := os.Open(sm.settingsPath)
	if err != nil {
		return "", fmt.Errorf("failed to open source file: %w", err)
	}
	defer src.Close()

	dst, err := os.Create(backupPath)
	if err != nil {
		return "", fmt.Errorf("failed to create backup file: %w", err)
	}
	defer dst.Close()

	if _, err := io.Copy(dst, src); err != nil {
		return "", fmt.Errorf("failed to copy file: %w", err)
	}

	// Create backup metadata
	metadataPath := backupPath + ".meta"
	metadata := BackupInfo{
		OriginalPath: sm.settingsPath,
		BackupPath:   backupPath,
		Timestamp:    time.Now(),
		Version:      "1.0",
	}

	metadataBytes, _ := json.MarshalIndent(metadata, "", "  ")
	if err := os.WriteFile(metadataPath, metadataBytes, 0644); err != nil {
		sm.log("Warning: failed to create backup metadata: %v", err)
	}

	sm.log("Created backup at %s", backupPath)
	return backupPath, nil
}

// MergeIrrlichtHooks adds Irrlicht hook configuration to settings
func (sm *SettingsMerger) MergeIrrlichtHooks() error {
	// Load current settings
	settings, err := sm.LoadSettings()
	if err != nil {
		return fmt.Errorf("failed to load settings: %w", err)
	}

	// Convert to JSON string for gjson/sjson operations
	settingsJSON, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("failed to marshal settings: %w", err)
	}

	// Define Irrlicht hook configuration
	irrlichtHook := HookConfig{
		Events: []string{
			"SessionStart",
			"UserPromptSubmit",
			"Notification", 
			"Stop",
			"SubagentStop",
			"SessionEnd",
		},
		Command: "irrlicht-hook",
	}

	// Check if hooks section exists
	hooksPath := "hooks"
	if !gjson.GetBytes(settingsJSON, hooksPath).Exists() {
		sm.log("Creating new hooks section")
		settingsJSON, err = sjson.SetBytes(settingsJSON, hooksPath, map[string]interface{}{})
		if err != nil {
			return fmt.Errorf("failed to create hooks section: %w", err)
		}
	}

	// Check if Irrlicht hook already exists
	irrlichtPath := "hooks.irrlicht"
	existingHook := gjson.GetBytes(settingsJSON, irrlichtPath)
	
	if existingHook.Exists() {
		sm.log("Irrlicht hook already exists, checking if update needed")
		
		// Compare existing hook with new configuration
		var existing HookConfig
		if err := json.Unmarshal([]byte(existingHook.Raw), &existing); err == nil {
			if sm.hooksEqual(existing, irrlichtHook) {
				sm.log("Existing hook configuration is identical, no changes needed")
				return nil
			}
		}
		sm.log("Updating existing hook configuration")
	} else {
		sm.log("Adding new Irrlicht hook configuration")
	}

	// Add/update the Irrlicht hook
	settingsJSON, err = sjson.SetBytes(settingsJSON, irrlichtPath, irrlichtHook)
	if err != nil {
		return fmt.Errorf("failed to set Irrlicht hook: %w", err)
	}

	// Write updated settings
	if err := sm.writeSettings(settingsJSON); err != nil {
		return fmt.Errorf("failed to write settings: %w", err)
	}

	sm.log("Successfully merged Irrlicht hook configuration")
	return nil
}

// RemoveIrrlichtHooks removes Irrlicht hook configuration from settings
func (sm *SettingsMerger) RemoveIrrlichtHooks() error {
	settings, err := sm.LoadSettings()
	if err != nil {
		return fmt.Errorf("failed to load settings: %w", err)
	}

	settingsJSON, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("failed to marshal settings: %w", err)
	}

	irrlichtPath := "hooks.irrlicht"
	if !gjson.GetBytes(settingsJSON, irrlichtPath).Exists() {
		sm.log("Irrlicht hook not found, nothing to remove")
		return nil
	}

	// Remove the Irrlicht hook
	settingsJSON, err = sjson.DeleteBytes(settingsJSON, irrlichtPath)
	if err != nil {
		return fmt.Errorf("failed to remove Irrlicht hook: %w", err)
	}

	// If hooks section is now empty, remove it entirely
	hooksResult := gjson.GetBytes(settingsJSON, "hooks")
	if hooksResult.Exists() {
		hooksMap := hooksResult.Map()
		if len(hooksMap) == 0 {
			sm.log("Hooks section is empty, removing it")
			settingsJSON, err = sjson.DeleteBytes(settingsJSON, "hooks")
			if err != nil {
				return fmt.Errorf("failed to remove empty hooks section: %w", err)
			}
		}
	}

	if err := sm.writeSettings(settingsJSON); err != nil {
		return fmt.Errorf("failed to write settings: %w", err)
	}

	sm.log("Successfully removed Irrlicht hook configuration")
	return nil
}

// RestoreFromBackup restores settings from a backup file
func (sm *SettingsMerger) RestoreFromBackup(backupPath string) error {
	if _, err := os.Stat(backupPath); os.IsNotExist(err) {
		return fmt.Errorf("backup file does not exist: %s", backupPath)
	}

	if sm.dryRun {
		sm.log("DRY RUN: Would restore from backup %s", backupPath)
		return nil
	}

	// Validate backup file is valid JSON
	backupData, err := os.ReadFile(backupPath)
	if err != nil {
		return fmt.Errorf("failed to read backup file: %w", err)
	}

	var testSettings map[string]interface{}
	if err := json.Unmarshal(backupData, &testSettings); err != nil {
		return fmt.Errorf("backup file contains invalid JSON: %w", err)
	}

	// Create backup directory if needed
	settingsDir := filepath.Dir(sm.settingsPath)
	if err := os.MkdirAll(settingsDir, 0755); err != nil {
		return fmt.Errorf("failed to create settings directory: %w", err)
	}

	// Copy backup to settings location
	if err := os.WriteFile(sm.settingsPath, backupData, 0644); err != nil {
		return fmt.Errorf("failed to restore settings file: %w", err)
	}

	sm.log("Successfully restored settings from backup %s", backupPath)
	return nil
}

// ListBackups finds all backup files for the settings
func (sm *SettingsMerger) ListBackups() ([]string, error) {
	pattern := sm.settingsPath + ".backup_*"
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("failed to list backups: %w", err)
	}
	
	// Filter out metadata files
	var backups []string
	for _, match := range matches {
		if !strings.HasSuffix(match, ".meta") {
			backups = append(backups, match)
		}
	}
	
	return backups, nil
}

// GetPreview generates a preview of what changes would be made
func (sm *SettingsMerger) GetPreview() (string, error) {
	originalDryRun := sm.dryRun
	sm.dryRun = true
	defer func() { sm.dryRun = originalDryRun }()

	// Load current settings
	current, err := sm.LoadSettings()
	if err != nil {
		return "", fmt.Errorf("failed to load current settings: %w", err)
	}

	// Create a copy and perform merge
	settingsJSON, _ := json.Marshal(current)
	
	irrlichtHook := HookConfig{
		Events:  []string{"SessionStart", "UserPromptSubmit", "Notification", "Stop", "SubagentStop", "SessionEnd"},
		Command: "irrlicht-hook",
	}

	// Add hooks section if missing
	if !gjson.GetBytes(settingsJSON, "hooks").Exists() {
		settingsJSON, _ = sjson.SetBytes(settingsJSON, "hooks", map[string]interface{}{})
	}

	// Add Irrlicht hook
	settingsJSON, _ = sjson.SetBytes(settingsJSON, "hooks.irrlicht", irrlichtHook)

	// Parse the modified settings
	var modified map[string]interface{}
	json.Unmarshal(settingsJSON, &modified)

	// Generate diff
	currentBytes, _ := json.MarshalIndent(current, "", "  ")
	modifiedBytes, _ := json.MarshalIndent(modified, "", "  ")

	return fmt.Sprintf("BEFORE:\n%s\n\nAFTER:\n%s\n", string(currentBytes), string(modifiedBytes)), nil
}

// hooksEqual compares two hook configurations
func (sm *SettingsMerger) hooksEqual(a, b HookConfig) bool {
	if a.Command != b.Command || len(a.Events) != len(b.Events) {
		return false
	}
	
	eventMap := make(map[string]bool)
	for _, event := range a.Events {
		eventMap[event] = true
	}
	
	for _, event := range b.Events {
		if !eventMap[event] {
			return false
		}
	}
	
	return true
}

// writeSettings writes settings JSON to file with proper formatting
func (sm *SettingsMerger) writeSettings(settingsJSON []byte) error {
	if sm.dryRun {
		sm.log("DRY RUN: Would write settings to %s", sm.settingsPath)
		return nil
	}

	// Pretty format the JSON
	var prettyJSON map[string]interface{}
	if err := json.Unmarshal(settingsJSON, &prettyJSON); err != nil {
		return err
	}

	formattedJSON, err := json.MarshalIndent(prettyJSON, "", "  ")
	if err != nil {
		return err
	}

	// Ensure directory exists
	settingsDir := filepath.Dir(sm.settingsPath)
	if err := os.MkdirAll(settingsDir, 0755); err != nil {
		return fmt.Errorf("failed to create settings directory: %w", err)
	}

	// Atomic write: write to temp file then rename
	tempPath := sm.settingsPath + ".tmp"
	if err := os.WriteFile(tempPath, formattedJSON, 0644); err != nil {
		return fmt.Errorf("failed to write temp file: %w", err)
	}

	if err := os.Rename(tempPath, sm.settingsPath); err != nil {
		os.Remove(tempPath) // Clean up temp file
		return fmt.Errorf("failed to rename temp file: %w", err)
	}

	sm.log("Settings written to %s", sm.settingsPath)
	return nil
}