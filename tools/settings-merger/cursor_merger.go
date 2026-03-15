package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// CursorMerger handles safe merging of Cursor IDE hooks.json configuration.
type CursorMerger struct {
	hooksPath string
	dryRun    bool
	verbose   bool
}

// NewCursorMerger creates a new CursorMerger targeting ~/.cursor/hooks.json by default.
func NewCursorMerger(hooksPath string) *CursorMerger {
	return &CursorMerger{hooksPath: hooksPath}
}

// SetDryRun enables/disables dry run mode.
func (cm *CursorMerger) SetDryRun(enabled bool) {
	cm.dryRun = enabled
}

// SetVerbose enables/disables verbose logging.
func (cm *CursorMerger) SetVerbose(enabled bool) {
	cm.verbose = enabled
}

func (cm *CursorMerger) log(format string, args ...interface{}) {
	if cm.verbose {
		fmt.Printf("[cursor-merger] "+format+"\n", args...)
	}
}

// cursorHookEntry is the format Cursor expects in hooks.json arrays.
type cursorHookEntry struct {
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
	Type    string `json:"type,omitempty"`
}

// cursorHookEvents is the list of Cursor hook events to configure.
var cursorHookEvents = []string{
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

// loadHooks reads and parses ~/.cursor/hooks.json. Returns an empty object if absent.
func (cm *CursorMerger) loadHooks() ([]byte, error) {
	data, err := os.ReadFile(cm.hooksPath)
	if os.IsNotExist(err) {
		cm.log("hooks.json does not exist, will create new one")
		return []byte(`{"version":1,"hooks":{}}`), nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read hooks file: %w", err)
	}
	return data, nil
}

// CreateBackup creates a timestamped backup of hooks.json.
func (cm *CursorMerger) CreateBackup() (string, error) {
	if _, err := os.Stat(cm.hooksPath); os.IsNotExist(err) {
		cm.log("No existing hooks.json to backup")
		return "", nil
	}
	timestamp := time.Now().Format("20060102_150405.000")
	backupPath := fmt.Sprintf("%s.backup_%s", cm.hooksPath, timestamp)
	if cm.dryRun {
		cm.log("DRY RUN: Would create backup at %s", backupPath)
		return backupPath, nil
	}
	data, err := os.ReadFile(cm.hooksPath)
	if err != nil {
		return "", fmt.Errorf("failed to read hooks file for backup: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(backupPath), 0755); err != nil {
		return "", fmt.Errorf("failed to create backup directory: %w", err)
	}
	if err := os.WriteFile(backupPath, data, 0644); err != nil {
		return "", fmt.Errorf("failed to write backup: %w", err)
	}
	cm.log("Created backup at %s", backupPath)
	return backupPath, nil
}

// MergeCursorHooks adds cursor-hook configuration to ~/.cursor/hooks.json.
// The operation is idempotent: if cursor-hook is already configured for an
// event, that event is skipped.
func (cm *CursorMerger) MergeCursorHooks() error {
	hooksJSON, err := cm.loadHooks()
	if err != nil {
		return err
	}

	entry := cursorHookEntry{
		Command: "cursor-hook",
		Timeout: 30,
		Type:    "command",
	}
	entryJSON, _ := json.Marshal(entry)

	for _, eventName := range cursorHookEvents {
		path := "hooks." + eventName
		existing := gjson.GetBytes(hooksJSON, path)

		if existing.Exists() && existing.IsArray() {
			// Check if cursor-hook is already registered.
			found := false
			for _, item := range existing.Array() {
				if item.Get("command").String() == "cursor-hook" {
					found = true
					break
				}
			}
			if found {
				cm.log("cursor-hook already configured for %s", eventName)
				continue
			}
			// Append our entry to the existing array.
			raw := existing.Raw
			// Build new array by appending entry.
			newArray := raw[:len(raw)-1] + "," + string(entryJSON) + "]"
			hooksJSON, err = sjson.SetRawBytes(hooksJSON, path, []byte(newArray))
		} else {
			// Create new array with just our entry.
			hooksJSON, err = sjson.SetRawBytes(hooksJSON, path, []byte("["+string(entryJSON)+"]"))
		}
		if err != nil {
			return fmt.Errorf("failed to set hook for %s: %w", eventName, err)
		}
		cm.log("Configured cursor-hook for %s", eventName)
	}

	return cm.writeHooks(hooksJSON)
}

// RemoveCursorHooks removes cursor-hook entries from ~/.cursor/hooks.json.
func (cm *CursorMerger) RemoveCursorHooks() error {
	hooksJSON, err := cm.loadHooks()
	if err != nil {
		return err
	}

	for _, eventName := range cursorHookEvents {
		path := "hooks." + eventName
		existing := gjson.GetBytes(hooksJSON, path)
		if !existing.Exists() || !existing.IsArray() {
			continue
		}

		// Rebuild array without cursor-hook entries.
		var kept []json.RawMessage
		for _, item := range existing.Array() {
			if item.Get("command").String() != "cursor-hook" {
				kept = append(kept, json.RawMessage(item.Raw))
			}
		}

		if len(kept) == 0 {
			hooksJSON, err = sjson.DeleteBytes(hooksJSON, path)
		} else {
			arr, _ := json.Marshal(kept)
			hooksJSON, err = sjson.SetRawBytes(hooksJSON, path, arr)
		}
		if err != nil {
			return fmt.Errorf("failed to remove hook for %s: %w", eventName, err)
		}
		cm.log("Removed cursor-hook from %s", eventName)
	}

	return cm.writeHooks(hooksJSON)
}

// ListBackups finds all backup files for hooks.json.
func (cm *CursorMerger) ListBackups() ([]string, error) {
	matches, err := filepath.Glob(cm.hooksPath + ".backup_*")
	if err != nil {
		return nil, fmt.Errorf("failed to list backups: %w", err)
	}
	var backups []string
	for _, m := range matches {
		if !strings.HasSuffix(m, ".meta") {
			backups = append(backups, m)
		}
	}
	return backups, nil
}

func (cm *CursorMerger) writeHooks(data []byte) error {
	if cm.dryRun {
		cm.log("DRY RUN: Would write hooks to %s", cm.hooksPath)
		return nil
	}

	// Pretty-format the output.
	var v interface{}
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	pretty, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(cm.hooksPath), 0755); err != nil {
		return fmt.Errorf("failed to create .cursor directory: %w", err)
	}

	tmpPath := cm.hooksPath + ".tmp"
	if err := os.WriteFile(tmpPath, pretty, 0644); err != nil {
		return fmt.Errorf("failed to write temp file: %w", err)
	}
	if err := os.Rename(tmpPath, cm.hooksPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to rename temp file: %w", err)
	}
	cm.log("Wrote hooks.json to %s", cm.hooksPath)
	return nil
}
