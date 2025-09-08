# Hook Contract Fixtures

This directory contains JSON samples for each Claude Code hook event type used by Irrlicht.

## Event Types

### Core Lifecycle Events

- **SessionStart** - Fired when a new Claude Code session begins
- **UserPromptSubmit** - User submits a prompt/message
- **Notification** - Claude needs user input or attention
- **Stop** - Session stops/pauses
- **SubagentStop** - Subagent execution stops
- **SessionEnd** - Session terminates

## Schema Structure

All hook events follow this base structure:

```json
{
  "hook_event_name": "EventType",
  "session_id": "string",
  "timestamp": "ISO8601",
  "data": {
    // Event-specific fields
    "transcript_path": "/path/to/transcript.jsonl",
    "cwd": "/working/directory",
    "model": "claude-3.7-sonnet",
    // ... other fields
  }
}
```

## State Mapping

Events map to Irrlicht states as follows:

- `SessionStart`, `UserPromptSubmit` → **working**
- `Notification` → **waiting** 
- `Stop`, `SubagentStop`, `SessionEnd` → **ready**

## Edge Cases

The `edge-cases/` directory contains fixtures for error conditions:

- **malformed-json.txt** - Invalid JSON syntax
- **oversized-payload.json** - >512KB payload (should be rejected)
- **missing-fields.json** - Required fields missing
- **invalid-paths.json** - Paths outside user domain

## Usage

These fixtures are used by `irrlicht-replay` for testing:

```bash
# Single event
cat fixtures/session-start.json | irrlicht-replay

# Multi-event scenario
irrlicht-replay --scenario fixtures/scenarios/concurrent-2.json
```