# Settings Merger

A robust library and CLI tool for safely managing Claude Code hook configurations in `~/.claude/settings.json`.

## Features

- **JSON-aware deep merge** - Preserves existing settings structure
- **Idempotent operations** - Running multiple times produces same result  
- **Atomic writes** - Never produces partial/corrupted files
- **Automatic backups** - Timestamped backups before any changes
- **Dry run mode** - Preview changes without applying them
- **Surgical removal** - Remove only Irrlicht hooks, leave others intact
- **Rollback capability** - Restore from any backup

## Building

```bash
cd tools/settings-merger
go mod tidy
go build -o settings-merger .
```

## Usage

### Merge hooks (install)
```bash
./settings-merger --action merge
```

### Preview changes
```bash
./settings-merger --action preview
```

### Remove hooks (uninstall)
```bash
./settings-merger --action remove
```

### Restore from backup
```bash
./settings-merger --action restore --backup ~/.claude/settings.json.backup_20240905_143000
```

### List available backups
```bash
./settings-merger --action list-backups
```

### Dry run mode
```bash
./settings-merger --dry-run --verbose
```

## Hook Configuration

The tool adds this configuration to Claude settings:

```json
{
  "hooks": {
    "irrlicht": {
      "events": [
        "SessionStart",
        "UserPromptSubmit", 
        "Notification",
        "Stop",
        "SubagentStop",
        "SessionEnd"
      ],
      "command": "irrlicht-hook"
    }
  }
}
```

## Safety Guarantees

- **Idempotent**: Multiple runs produce identical results
- **Reversible**: Backup → modify → restore leaves settings identical to original
- **Non-destructive**: Never corrupts existing settings.json structure
- **Atomic**: Either fully succeeds or fails without partial changes
- **Validated**: All JSON is validated before writing

## Error Handling

- Invalid JSON in settings file → clear error message
- Missing directories → automatically created
- Permission errors → graceful failure with explanation
- Malformed backup files → validation before restore
- Concurrent access → atomic operations prevent corruption

## Kill Switch Integration

The merger respects these disable mechanisms:

- Environment variable: `IRRLICHT_DISABLED=1`
- Settings flag: `hooks.irrlicht.disabled: true`

When disabled, hooks are present but inactive.