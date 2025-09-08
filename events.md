# Claude Code Events and State Transitions

This document lists all Claude Code events and their resulting state transitions, based on official documentation and research as of 2025.

## Primary Session States

- **`working`** - Claude actively processing/executing
- **`waiting`** - Waiting for user input or permission
- **`ready`** - No session started or task ready

## Official Hook Events (9 Total)

| Event | Trigger | State Transition | Hook Available | Can Block |
|-------|---------|------------------|----------------|-----------|
| **SessionStart** | New session starts or resumes | `idle` → `working` | ✅ Yes | ❌ No |
| **UserPromptSubmit** | User submits prompt (before processing) | `waiting` → `working` | ✅ Yes | ✅ Yes |
| **PreToolUse** | Before any tool execution | `working` → `working` | ✅ Yes | ✅ Yes |
| **PostToolUse** | After successful tool completion | `working` → `working` | ✅ Yes | ❌ No |
| **Notification** | Claude needs permission/input | `working` → `waiting` | ✅ Yes | ❌ No |
| **PreCompact** | Before context compaction | `working` → `working` | ✅ Yes | ❌ No |
| **Stop** | Main agent finishes responding | `working` → `ready` | ✅ Yes | ✅ Yes |
| **SubagentStop** | Subagent task completes | `working` → `ready` | ✅ Yes | ✅ Yes |
| **SessionEnd** | Session terminates | `working/ready` → `idle` | ✅ Yes | ❌ No |

## Detectable Non-Hook Events

| Event | Trigger | State Transition | Detection Method | Frequency |
|-------|---------|------------------|------------------|-----------|
| **Session Resume** | User returns to existing session | `idle` → `working` | File system monitoring | Per session |
| **User Interrupt** | Ctrl+C or ESC pressed | `working` → `ready` | Signal monitoring | User action |
| **Context Full** | Token limit approaching | `working` → `working` (triggers auto-compact) | Token counting | Automatic |
| **Tool Timeout** | Tool execution exceeds limit | `working` → `error` | Process monitoring | Error condition |
| **Tool Error** | Tool fails with exception | `working` → `working` (error reported) | Error log monitoring | Error condition |
| **API Timeout** | Network request times out | `working` → `error` | HTTP monitoring | Network issue |
| **API Rate Limit** | Too many requests | `working` → `waiting` (throttled) | HTTP response codes | Rate limiting |
| **Network Disconnect** | Internet connection lost | `working` → `error` | Network monitoring | Connection issue |
| **Transcript Write** | New log entry added | `waiting` → `working` | File monitoring | Every interaction |
| **Config Change** | Settings file modified | No state change | File watching | Configuration |
| **Process Start** | Claude Code launches | `ready` → `ready` (ready) | Process monitoring | Application launch |
| **Process Exit** | Claude Code terminates | Any state → delete session | Process monitoring | Application exit |

## Detailed State Flow

```
Application Launch
    ↓
  ready ←─────────────────────────────────┐
    ↓ SessionStart                        │
 working ←─────────────┐                  │
    ↓ Notification     │ UserPromptSubmit │ SessionEnd
 waiting ──────────────┘                  │
    ↓ Stop/SubagentStop                   │
  ready ──────────────────────────────────┘
    ↓ Error conditions
  error → (manual recovery) → ready
```

## Hook Event Details

### SessionStart
- **Fires**: When starting new session or resuming existing
- **Data**: `source` (startup/resume/clear/compact), `session_id`, `transcript_path`
- **Blocking**: Cannot block, shows stderr only
- **State**: `idle` → `working`

### UserPromptSubmit  
- **Fires**: Before Claude processes user input
- **Data**: `prompt`, `session_id`, `transcript_path`  
- **Blocking**: Can block with exit code 2
- **State**: `waiting` → `working`

### PreToolUse
- **Fires**: After tool parameters created, before execution
- **Data**: `tool_name`, `tool_input`, `session_id`
- **Blocking**: Can block with exit code 2 or JSON response
- **State**: `working` → `working` (maintained)

### PostToolUse
- **Fires**: Immediately after successful tool completion
- **Data**: `tool_name`, `tool_input`, `tool_response`
- **Blocking**: Cannot block, informational only
- **State**: `working` → `working` (continued)

### Notification
- **Fires**: When Claude needs permission or user is idle (60s+)
- **Data**: `message`, `notification_type`
- **Blocking**: Cannot block, informational only
- **State**: `working` → `waiting`

### PreCompact
- **Fires**: Before manual `/compact` or automatic compaction
- **Data**: `compact_type` (manual/auto)
- **Blocking**: Cannot block, shows stderr only  
- **State**: `working` → `working` (maintained during compaction)

### Stop
- **Fires**: When main Claude agent completes response
- **Data**: `session_id`, response completion info
- **Blocking**: Can block with exit code 2
- **State**: `working` → `ready`

### SubagentStop  
- **Fires**: When subagent (Task tool) completes
- **Data**: `subagent_id`, completion status
- **Blocking**: Can block with exit code 2
- **State**: `working` → `ready`

### SessionEnd
- **Fires**: When session terminates
- **Data**: `exit_reason` (clear/logout/prompt_input_exit/other)
- **Blocking**: Cannot block, shows stderr only
- **State**: Any state → kill session

## Tool-Specific Events (via PreToolUse/PostToolUse)

| Tool | PreToolUse Triggers | PostToolUse Triggers | Common State Flow |
|------|-------------------|---------------------|-------------------|
| **Bash** | Before command execution | After command completes | `working` → `working` |
| **Read** | Before file read | After file read | `working` → `working` |
| **Write/Edit** | Before file write | After file written | `working` → `working` |
| **Task** | Before subagent spawn | After subagent completes | `working` → `working` → `ready` |
| **WebFetch** | Before HTTP request | After response received | `working` → `working` |
| **Grep/Glob** | Before search | After search results | `working` → `working` |

## Hook Matchers and Filters

### Session Source Matchers (SessionStart)
- `startup` - Fresh application start → `ready`
- `resume` - Resuming existing session → `ready`
- `clear` - After session cleared → `ready`
- `compact` - After context compaction → `ready`

### Compact Type Matchers (PreCompact)
- `manual` - User triggered `/compact` → `working`
- `auto` - Automatic when context full → `working`

### Tool Matchers (PreToolUse/PostToolUse)
- `Task` - Subagent operations → `working`
- `Bash` - Shell commands → `working`
- `Read/Write/Edit` - File operations → `working`
- `WebFetch/WebSearch` - Web requests → `working`
- `Grep/Glob` - Search operations → `working`

## State Persistence

| State | Persisted Where | Duration | Recovery Method |
|-------|----------------|----------|-----------------|
| `working` | Session state files | Until completion | Resume or timeout |
| `waiting` | Session state files | Until user input | User response |
| `ready` | Session state files | Until new input | New prompt |
| `error` | Error logs | Until resolved | Manual intervention |

## Hook Configuration

### Exit Codes
- **0** - Continue normally
- **1** - Show stderr, continue  
- **2** - Block execution (where supported)

### JSON Control (Advanced)
```json
{
  "continue": true/false,
  "stopReason": "message",
  "suppressOutput": true/false,
  "systemMessage": "warning"
}
```

### Tool-Specific Control (PreToolUse)
```json
{
  "permissionDecision": "allow/deny",
  "feedback": "message to Claude"
}
```

## Integration Notes

- **File Locations**: State files in `~/Library/Application Support/Irrlicht/instances/`
- **Environment**: `CLAUDE_PROJECT_DIR` available in all hooks
- **Timeout**: 60 seconds default hook execution limit
- **Parallel**: Multiple hooks run simultaneously
- **Kill Switch**: `IRRLICHT_DISABLED=1` disables all hooks

---

*Based on official Anthropic Claude Code documentation and research - September 2025*