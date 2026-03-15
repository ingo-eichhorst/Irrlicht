package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// hookStanza is a single hook entry in the Cursor hooks config.
type hookStanza struct {
	Command     string `json:"command"`
	Timeout     int    `json:"timeout"`
	Type        string `json:"type"`
	FailClosed  bool   `json:"failClosed,omitempty"`
}

// cursorHooksConfig is the structure of ~/.cursor/hooks.json.
type cursorHooksConfig struct {
	Version  int                     `json:"version"`
	Hooks    map[string][]hookStanza `json:"hooks,omitempty"`
	Disabled bool                    `json:"disabled,omitempty"`
}

// cursorEvents is the ordered list of events Cursor IDE fires.
var cursorEvents = []string{
	"sessionStart",
	"sessionEnd",
	"stop",
	"subagentStart",
	"subagentStop",
	"preToolUse",
	"postToolUse",
	"postToolUseFailure",
	"beforeSubmitPrompt",
	"preCompact",
	"afterAgentThought",
	"beforeShellExecution",
	"afterShellExecution",
}

// HooksMerger manages the Cursor hook configuration file.
type HooksMerger struct {
	configPath string
	dryRun     bool
	verbose    bool
}

// NewHooksMerger creates a new HooksMerger for the given config file path.
func NewHooksMerger(configPath string) *HooksMerger {
	return &HooksMerger{configPath: configPath}
}

// SetDryRun enables or disables dry-run mode.
func (m *HooksMerger) SetDryRun(v bool) { m.dryRun = v }

// SetVerbose enables or disables verbose logging.
func (m *HooksMerger) SetVerbose(v bool) { m.verbose = v }

func (m *HooksMerger) log(format string, args ...interface{}) {
	if m.verbose {
		fmt.Printf("[merger] "+format+"\n", args...)
	}
}

// CreateBackup creates a timestamped copy of the config file.
// Returns the backup path (empty string if no existing config to back up).
func (m *HooksMerger) CreateBackup() (string, error) {
	if _, err := os.Stat(m.configPath); os.IsNotExist(err) {
		m.log("No existing config file to backup")
		return "", nil
	}

	timestamp := time.Now().Format("20060102_150405.000")
	backupPath := fmt.Sprintf("%s.backup_%s", m.configPath, timestamp)

	if m.dryRun {
		m.log("DRY RUN: Would create backup at %s", backupPath)
		return backupPath, nil
	}

	if err := os.MkdirAll(filepath.Dir(backupPath), 0755); err != nil {
		return "", fmt.Errorf("failed to create backup directory: %w", err)
	}

	src, err := os.Open(m.configPath)
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

	m.log("Created backup at %s", backupPath)
	return backupPath, nil
}

// ListBackups returns all backup file paths for the config.
func (m *HooksMerger) ListBackups() ([]string, error) {
	matches, err := filepath.Glob(m.configPath + ".backup_*")
	if err != nil {
		return nil, fmt.Errorf("failed to list backups: %w", err)
	}
	var backups []string
	for _, match := range matches {
		if !strings.HasSuffix(match, ".meta") {
			backups = append(backups, match)
		}
	}
	return backups, nil
}

// RestoreFromBackup overwrites the config with data from the given backup file.
func (m *HooksMerger) RestoreFromBackup(backupPath string) error {
	if _, err := os.Stat(backupPath); os.IsNotExist(err) {
		return fmt.Errorf("backup file does not exist: %s", backupPath)
	}
	if m.dryRun {
		m.log("DRY RUN: Would restore from %s", backupPath)
		return nil
	}
	data, err := os.ReadFile(backupPath)
	if err != nil {
		return fmt.Errorf("failed to read backup file: %w", err)
	}
	// Validate it's valid JSON before restoring.
	var check map[string]interface{}
	if err := json.Unmarshal(data, &check); err != nil {
		return fmt.Errorf("backup file contains invalid JSON: %w", err)
	}
	return m.writeRaw(data)
}

// MergeHooks writes the complete Irrlicht hook stanzas to the config file.
func (m *HooksMerger) MergeHooks() error {
	cfg := m.buildConfig(false)
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}
	return m.writeRaw(data)
}

// MergeDisable writes the config with disabled=true (activates kill switch).
func (m *HooksMerger) MergeDisable() error {
	cfg := m.buildConfig(true)
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}
	return m.writeRaw(data)
}

// Remove deletes the Irrlicht Cursor hooks config file.
func (m *HooksMerger) Remove() error {
	if _, err := os.Stat(m.configPath); os.IsNotExist(err) {
		m.log("Config file does not exist, nothing to remove")
		return nil
	}
	if m.dryRun {
		m.log("DRY RUN: Would remove %s", m.configPath)
		return nil
	}
	if err := os.Remove(m.configPath); err != nil {
		return fmt.Errorf("failed to remove config file: %w", err)
	}
	m.log("Removed %s", m.configPath)
	return nil
}

// GetPreview returns a JSON string showing what MergeHooks would write.
func (m *HooksMerger) GetPreview() (string, error) {
	cfg := m.buildConfig(false)
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// buildConfig constructs the cursorHooksConfig struct.
func (m *HooksMerger) buildConfig(disabled bool) *cursorHooksConfig {
	hooks := make(map[string][]hookStanza, len(cursorEvents))
	for _, event := range cursorEvents {
		hooks[event] = []hookStanza{
			{
				Command: "irrlicht-hook-cursor",
				Timeout: 30,
				Type:    "command",
			},
		}
	}
	return &cursorHooksConfig{
		Version:  1,
		Hooks:    hooks,
		Disabled: disabled,
	}
}

// writeRaw writes raw bytes to the config file atomically.
func (m *HooksMerger) writeRaw(data []byte) error {
	if m.dryRun {
		m.log("DRY RUN: Would write to %s:\n%s", m.configPath, string(data))
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(m.configPath), 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	tmpPath := m.configPath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write temp file: %w", err)
	}

	if err := os.Rename(tmpPath, m.configPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to rename temp file: %w", err)
	}

	m.log("Config written to %s", m.configPath)
	return nil
}
