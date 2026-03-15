// cursor-hooks-merger installs and manages the Irrlicht Cursor IDE hook configuration.
//
// It writes (or removes) ~/.cursor/hooks.json, registering irrlicht-hook-cursor
// for all Cursor IDE hook events. The pattern mirrors copilot-hooks-merger.
//
// Usage:
//
//	cursor-hooks-merger [options]
//
// Actions: merge (default), merge-disable, remove, restore, list-backups, preview.
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
		configPath = flag.String("config", "", "Path to hooks config (default: ~/.cursor/hooks.json)")
		action     = flag.String("action", "merge", "Action: merge, merge-disable, remove, restore, list-backups, preview")
		backupPath = flag.String("backup", "", "Path to backup file (for restore action)")
		dryRun     = flag.Bool("dry-run", false, "Show what would be done without making changes")
		verbose    = flag.Bool("verbose", false, "Enable verbose output")
		help       = flag.Bool("help", false, "Show help")
	)
	flag.Parse()

	if *help {
		showHelp()
		return
	}

	if *configPath == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: could not determine home directory: %v\n", err)
			os.Exit(1)
		}
		*configPath = filepath.Join(homeDir, ".cursor", "hooks.json")
	}

	merger := NewHooksMerger(*configPath)
	merger.SetDryRun(*dryRun)
	merger.SetVerbose(*verbose)

	switch strings.ToLower(*action) {
	case "merge":
		if err := performMerge(merger); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "merge-disable":
		backupPath, err := merger.CreateBackup()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to create backup: %v\n", err)
			os.Exit(1)
		}
		if backupPath != "" {
			fmt.Printf("Created backup: %s\n", backupPath)
		}
		if err := merger.MergeDisable(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Irrlicht Cursor hooks disabled (kill switch active)")

	case "remove":
		if err := performRemove(merger); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "restore":
		if *backupPath == "" {
			fmt.Fprintln(os.Stderr, "Error: --backup path required for restore action")
			os.Exit(1)
		}
		if err := merger.RestoreFromBackup(*backupPath); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Successfully restored config from %s\n", *backupPath)

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
			for _, b := range backups {
				fmt.Printf("  %s\n", b)
			}
		}

	case "preview":
		preview, err := merger.GetPreview()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Preview of hooks config that would be written:")
		fmt.Println(strings.Repeat("=", 50))
		fmt.Println(preview)

	default:
		fmt.Fprintf(os.Stderr, "Error: unknown action %q. Use --help for usage.\n", *action)
		os.Exit(1)
	}
}

func performMerge(merger *HooksMerger) error {
	backupPath, err := merger.CreateBackup()
	if err != nil {
		return fmt.Errorf("failed to create backup: %w", err)
	}
	if backupPath != "" {
		fmt.Printf("Created backup: %s\n", backupPath)
	}
	if err := merger.MergeHooks(); err != nil {
		return fmt.Errorf("failed to write hooks config: %w", err)
	}
	fmt.Println("Successfully installed Irrlicht Cursor hook configuration")
	return nil
}

func performRemove(merger *HooksMerger) error {
	backupPath, err := merger.CreateBackup()
	if err != nil {
		return fmt.Errorf("failed to create backup: %w", err)
	}
	if backupPath != "" {
		fmt.Printf("Created backup: %s\n", backupPath)
	}
	if err := merger.Remove(); err != nil {
		return fmt.Errorf("failed to remove config: %w", err)
	}
	fmt.Println("Successfully removed Irrlicht Cursor hook configuration")
	return nil
}

func showHelp() {
	fmt.Println("Irrlicht Cursor Hooks Merger — Manage Cursor IDE hook configuration")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  cursor-hooks-merger [options]")
	fmt.Println()
	fmt.Println("Actions:")
	fmt.Println("  merge          Install Irrlicht hooks for all Cursor events (default)")
	fmt.Println("  merge-disable  Install config with disabled=true (kill switch)")
	fmt.Println("  remove         Remove the Irrlicht hooks config file")
	fmt.Println("  restore        Restore config from a backup file")
	fmt.Println("  list-backups   List available backup files")
	fmt.Println("  preview        Show what config would be written")
	fmt.Println()
	fmt.Println("Options:")
	fmt.Println("  --config PATH    Path to hooks config (default: ~/.cursor/hooks.json)")
	fmt.Println("  --action ACTION  Action to perform")
	fmt.Println("  --backup PATH    Backup file path (for restore)")
	fmt.Println("  --dry-run        Show changes without applying them")
	fmt.Println("  --verbose        Enable verbose output")
	fmt.Println("  --help           Show this help")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  cursor-hooks-merger                                  # Install hooks")
	fmt.Println("  cursor-hooks-merger --action preview                 # Preview config")
	fmt.Println("  cursor-hooks-merger --action remove                  # Remove hooks")
	fmt.Println("  cursor-hooks-merger --dry-run                        # Test run")
	fmt.Println("  cursor-hooks-merger --action restore --backup file   # Restore backup")
}
