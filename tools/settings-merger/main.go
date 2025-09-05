package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	var (
		settingsPath = flag.String("settings", "", "Path to Claude settings.json file (default: ~/.claude/settings.json)")
		action       = flag.String("action", "merge", "Action: merge, remove, restore, list-backups, preview")
		backupPath   = flag.String("backup", "", "Path to backup file (for restore action)")
		dryRun       = flag.Bool("dry-run", false, "Show what would be done without making changes")
		verbose      = flag.Bool("verbose", false, "Enable verbose output")
		help         = flag.Bool("help", false, "Show help")
	)
	flag.Parse()

	if *help {
		showHelp()
		return
	}

	// Default settings path
	if *settingsPath == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: Could not determine home directory: %v\n", err)
			os.Exit(1)
		}
		*settingsPath = filepath.Join(homeDir, ".claude", "settings.json")
	}

	// Create merger
	merger := NewSettingsMerger(*settingsPath)
	merger.SetDryRun(*dryRun)
	merger.SetVerbose(*verbose)

	// Execute action
	switch strings.ToLower(*action) {
	case "merge":
		if err := performMerge(merger); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "remove":
		if err := performRemove(merger); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "restore":
		if *backupPath == "" {
			fmt.Fprintf(os.Stderr, "Error: --backup path required for restore action\n")
			os.Exit(1)
		}
		if err := merger.RestoreFromBackup(*backupPath); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Successfully restored settings from %s\n", *backupPath)

	case "list-backups":
		backups, err := merger.ListBackups()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		if len(backups) == 0 {
			fmt.Println("No backups found")
		} else {
			fmt.Printf("Found %d backup(s):\n", len(backups))
			for _, backup := range backups {
				fmt.Printf("  %s\n", backup)
			}
		}

	case "preview":
		preview, err := merger.GetPreview()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Preview of changes:")
		fmt.Println(strings.Repeat("=", 50))
		fmt.Print(preview)

	default:
		fmt.Fprintf(os.Stderr, "Error: Unknown action '%s'. Use --help for usage.\n", *action)
		os.Exit(1)
	}
}

func performMerge(merger *SettingsMerger) error {
	// Create backup first
	backupPath, err := merger.CreateBackup()
	if err != nil {
		return fmt.Errorf("failed to create backup: %w", err)
	}

	if backupPath != "" {
		fmt.Printf("Created backup: %s\n", backupPath)
	}

	// Perform merge
	if err := merger.MergeIrrlichtHooks(); err != nil {
		return fmt.Errorf("failed to merge hooks: %w", err)
	}

	fmt.Println("Successfully merged Irrlicht hook configuration")
	return nil
}

func performRemove(merger *SettingsMerger) error {
	// Create backup first
	backupPath, err := merger.CreateBackup()
	if err != nil {
		return fmt.Errorf("failed to create backup: %w", err)
	}

	if backupPath != "" {
		fmt.Printf("Created backup: %s\n", backupPath)
	}

	// Perform removal
	if err := merger.RemoveIrrlichtHooks(); err != nil {
		return fmt.Errorf("failed to remove hooks: %w", err)
	}

	fmt.Println("Successfully removed Irrlicht hook configuration")
	return nil
}

func showHelp() {
	fmt.Println("Irrlicht Settings Merger - Safely manage Claude Code hook configuration")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  settings-merger [options]")
	fmt.Println()
	fmt.Println("Actions:")
	fmt.Println("  merge         Add Irrlicht hooks to settings (default)")
	fmt.Println("  remove        Remove Irrlicht hooks from settings")  
	fmt.Println("  restore       Restore settings from backup file")
	fmt.Println("  list-backups  List available backup files")
	fmt.Println("  preview       Show what changes would be made")
	fmt.Println()
	fmt.Println("Options:")
	fmt.Println("  --settings PATH    Path to settings.json (default: ~/.claude/settings.json)")
	fmt.Println("  --action ACTION    Action to perform")
	fmt.Println("  --backup PATH      Backup file path (for restore)")
	fmt.Println("  --dry-run          Show changes without applying them")
	fmt.Println("  --verbose          Enable verbose output")
	fmt.Println("  --help             Show this help")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  settings-merger                                    # Merge hooks")
	fmt.Println("  settings-merger --action preview                   # Preview changes")
	fmt.Println("  settings-merger --action remove                    # Remove hooks")
	fmt.Println("  settings-merger --dry-run                          # Test run")
	fmt.Println("  settings-merger --action restore --backup file     # Restore")
}