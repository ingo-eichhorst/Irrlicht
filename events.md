# Claude Code Complete Events & State Transitions Reference

## Overview

This document provides a comprehensive analysis of all events and state transitions that can occur in Claude Code, based on extensive research of the official documentation and community resources as of 2025. Events are categorized by their detection method and whether they trigger hooks.

## Complete Claude Code Events Table

| Event Category | Event Name | Description | When It Fires | Hook Available | Detection Method | Parameters | State Impact | Implementation Status |
|---|---|---|---|---|---|---|---|---|
| **SESSION LIFECYCLE** | | | | | | | | |
| Session Management | `SessionStart` | New session begins or resumes | Session startup, resume after clear/compact | ✅ Hook | Hook receiver | `source` (startup/resume/clear/compact), `session_id`, `transcript_path`, `cwd` | Creates session in working state | ✅ Implemented |
| Session Management | `SessionEnd` | Session terminates | User exits, logout, or session closes | ✅ Hook | Hook receiver | `reason` (clear/logout/prompt_input_exit/other), `session_id` | Marks session as finished | ✅ Implemented |
| Session Management | `SessionTimeout` | Session times out due to inactivity | Network timeout or API timeout | ❌ No Hook | Error monitoring | `timeout_duration`, `last_activity` | Session becomes disconnected | ❌ Not handled |
| Session Management | `SessionReconnect` | Session reconnects after timeout | Successful reconnection to API | ❌ No Hook | Connection monitoring | `reconnect_attempt`, `success` | Session resumes | ❌ Not handled |
| **USER INTERACTIONS** | | | | | | | | |
| User Input | `UserPromptSubmit` | User submits a prompt | Before Claude processes user input | ✅ Hook | Hook receiver | `prompt`, `session_id`, `transcript_path` | Transitions to working state | ✅ Implemented |
| User Input | `UserPromptEdit` | User edits their prompt | User modifies input before submitting | ❌ No Hook | Input field monitoring | `original_prompt`, `edited_prompt` | No state change | ❌ Not detected |
| User Input | `UserInterrupt` | User interrupts Claude (Ctrl+C) | User cancels Claude's response | ❌ No Hook | Signal monitoring | `interruption_method` | Transitions to waiting | ❌ Not detected |
| User Input | `UserIdle` | User inactive for extended period | 60+ seconds without input | 🟡 Notification | Notification system | `idle_duration` | Shows waiting notification | 🟡 Partial (notification only) |
| **AGENT RESPONSES** | | | | | | | | |
| Agent Lifecycle | `Stop` | Main agent finishes responding | Claude completes main response | ✅ Hook | Hook receiver | `stop_hook_active`, `session_id` | Transitions to finished/waiting | ✅ Implemented |
| Agent Lifecycle | `SubagentStart` | Subagent begins execution | Subagent spawned from main agent | ❌ No Hook | Task tracking | `subagent_id`, `parent_session`, `task_description` | Creates subagent context | ❌ Not detected |
| Agent Lifecycle | `SubagentStop` | Subagent completes task | Subagent finishes execution | ✅ Hook | Hook receiver | `stop_hook_active`, `subagent_id` | Subagent marked finished | ✅ Implemented |
| Agent Lifecycle | `Notification` | Claude needs user input/permission | Requesting permission or user input | ✅ Hook | Hook receiver | `message`, `notification_type` | Transitions to waiting state | ✅ Implemented |
| **TOOL EXECUTION** | | | | | | | | |
| Tool Lifecycle | `PreToolUse` | Before tool execution | After parameters created, before execution | ✅ Hook | Hook receiver | `tool_name`, `tool_input`, `session_id` | Maintains working state | ✅ Implemented |
| Tool Lifecycle | `PostToolUse` | After successful tool execution | Immediately after tool completes | ✅ Hook | Hook receiver | `tool_name`, `tool_input`, `tool_response` | Continues working state | ✅ Implemented |
| Tool Lifecycle | `ToolError` | Tool execution fails | Tool encounters error/exception | ❌ No Hook | Error log monitoring | `tool_name`, `error_type`, `error_message` | May transition to error state | ❌ Not detected |
| Tool Lifecycle | `ToolTimeout` | Tool execution times out | Tool exceeds time limit | ❌ No Hook | Timeout monitoring | `tool_name`, `timeout_duration` | Error state | ❌ Not detected |
| Tool Lifecycle | `ToolPermissionDenied` | Tool blocked by permissions | Insufficient permissions for tool | ❌ No Hook | Permission monitoring | `tool_name`, `permission_required` | Shows permission error | ❌ Not detected |
| **SPECIFIC TOOL EVENTS** | | | | | | | | |
| File Operations | `FileRead` | File read operation | Read tool executed | 🟡 PreToolUse | Tool hook | `file_path`, `content_preview` | No state change | ✅ Via PreToolUse |
| File Operations | `FileWrite` | File write operation | Write/Edit tool executed | 🟡 PreToolUse | Tool hook | `file_path`, `content` | No state change | ✅ Via PreToolUse |
| File Operations | `FileCreate` | New file created | File created via Write tool | 🟡 PostToolUse | Tool hook | `file_path`, `file_size` | No state change | ✅ Via PostToolUse |
| File Operations | `FileDelete` | File deleted | File removed via Bash/tool | 🟡 Tool Hook | Tool hook | `file_path` | No state change | ✅ Via tool hooks |
| Shell Operations | `BashCommand` | Shell command execution | Bash tool used | 🟡 PreToolUse | Tool hook | `command`, `working_directory` | No state change | ✅ Via PreToolUse |
| Shell Operations | `BashError` | Shell command fails | Non-zero exit code | 🟡 PostToolUse | Tool hook | `command`, `exit_code`, `stderr` | No state change | ✅ Via PostToolUse |
| Web Operations | `WebFetch` | Web content retrieved | WebFetch/WebSearch tool used | 🟡 PreToolUse | Tool hook | `url`, `method` | No state change | ✅ Via PreToolUse |
| Web Operations | `WebError` | Web request fails | HTTP error or network failure | 🟡 PostToolUse | Tool hook | `url`, `status_code`, `error` | No state change | ✅ Via PostToolUse |
| **CONTEXT MANAGEMENT** | | | | | | | | |
| Context Window | `PreCompact` | Before context compaction | Manual /compact or auto-compact | ✅ Hook | Hook receiver | `compact_type` (manual/auto), `tokens_before` | Working state maintained | ✅ Implemented |
| Context Window | `PostCompact` | After context compaction | Context successfully compacted | ❌ No Hook | Transcript monitoring | `tokens_after`, `compression_ratio` | Returns to previous state | ❌ Not detected |
| Context Window | `MicroCompact` | Micro-compact operation | Automatic old tool call removal | ❌ No Hook | Token monitoring | `tools_removed`, `tokens_saved` | Continues current state | ❌ Not detected |
| Context Window | `ContextFull` | Context window approaching limit | Near 200k token limit | ❌ No Hook | Token counting | `current_tokens`, `limit` | Triggers auto-compact | ❌ Not detected |
| **ERROR CONDITIONS** | | | | | | | | |
| API Errors | `APITimeout` | API request timeout | Request exceeds timeout limit | ❌ No Hook | Error monitoring | `request_id`, `timeout_duration` | Connection error state | ❌ Not detected |
| API Errors | `APIRateLimit` | Rate limit exceeded | Too many requests in time window | ❌ No Hook | HTTP response monitoring | `retry_after`, `limit_type` | Throttled state | ❌ Not detected |
| API Errors | `APIError` | General API error | HTTP 4xx/5xx responses | ❌ No Hook | HTTP monitoring | `status_code`, `error_message` | Error state | ❌ Not detected |
| System Errors | `NetworkDisconnect` | Network connectivity lost | Internet connection drops | ❌ No Hook | Network monitoring | `connection_type` | Offline state | ❌ Not detected |
| System Errors | `SystemResourceLimit` | System resource exhaustion | Memory/disk/CPU limits hit | ❌ No Hook | System monitoring | `resource_type`, `usage` | Resource constrained | ❌ Not detected |
| **CONFIGURATION EVENTS** | | | | | | | | |
| Settings | `ConfigChange` | Configuration modified | Settings file updated | ❌ No Hook | File watching | `config_path`, `changes` | May affect behavior | ❌ Not detected |
| Settings | `HookConfigChange` | Hook configuration changed | Hooks added/modified/removed | ❌ No Hook | Config file watching | `hook_changes` | Hook behavior changes | ❌ Not detected |
| Settings | `ProjectConfigLoad` | Project settings loaded | Project-specific config applied | ❌ No Hook | Config loading | `project_path`, `config` | Project state change | ❌ Not detected |
| **SUBAGENT EVENTS** | | | | | | | | |
| Subagent Lifecycle | `SubagentSpawn` | New subagent created | Main agent delegates to subagent | ❌ No Hook | Task system monitoring | `subagent_type`, `parent_id` | Subagent working state | ❌ Not detected |
| Subagent Lifecycle | `SubagentError` | Subagent encounters error | Subagent fails during execution | ❌ No Hook | Error monitoring | `subagent_id`, `error_details` | Subagent error state | ❌ Not detected |
| Subagent Lifecycle | `SubagentTimeout` | Subagent times out | Subagent exceeds execution limit | ❌ No Hook | Timeout monitoring | `subagent_id`, `timeout` | Subagent terminated | ❌ Not detected |
| **TRANSCRIPT EVENTS** | | | | | | | | |
| Logging | `TranscriptWrite` | New entry added to transcript | Every interaction logged | ❌ No Hook | File monitoring | `entry_type`, `content` | No state change | 🟡 File watching possible |
| Logging | `TranscriptRotate` | Transcript file rotated | Log rotation for size management | ❌ No Hook | File system monitoring | `old_file`, `new_file` | No state change | ❌ Not detected |
| Logging | `TranscriptCorrupt` | Transcript file corruption | JSONL parsing errors | ❌ No Hook | Parse error monitoring | `line_number`, `error` | May affect recovery | ❌ Not detected |
| **THINKING MODE EVENTS** | | | | | | | | |
| Cognitive Processing | `ThinkingModeStart` | Extended thinking begins | "think", "ultrathink" commands | ❌ No Hook | Command detection | `thinking_level`, `budget` | Enhanced processing | ❌ Not detected |
| Cognitive Processing | `ThinkingModeEnd` | Extended thinking completes | Thinking budget exhausted | ❌ No Hook | Response monitoring | `tokens_used`, `outcome` | Returns to normal | ❌ Not detected |
| **PERMISSION EVENTS** | | | | | | | | |
| Security | `PermissionRequest` | Tool requests permission | High-risk operations | ❌ No Hook | Security monitoring | `tool_name`, `risk_level` | Awaiting user approval | ❌ Not detected |
| Security | `PermissionGranted` | User grants permission | User approves risky operation | ❌ No Hook | User interaction | `permission_type` | Operation proceeds | ❌ Not detected |
| Security | `PermissionDenied` | User denies permission | User blocks risky operation | ❌ No Hook | User interaction | `permission_type` | Operation blocked | ❌ Not detected |

## Detection Method Legend

- **✅ Hook**: Official Claude Code hook available
- **🟡 Partial Hook**: Detectable through existing hooks (PreToolUse/PostToolUse)  
- **❌ No Hook**: No official hook, requires external monitoring

## State Transitions

Claude Code operates with these primary states:

1. **Idle**: No active session
2. **Working**: Claude is processing/thinking/executing
3. **Waiting**: Waiting for user input or permission
4. **Finished**: Response complete, ready for next input
5. **Error**: Error state requiring attention
6. **Offline**: No connection to Claude API

## Current Irrlicht Implementation Status

- **✅ Fully Implemented**: 9 events (core hook events)
- **🟡 Partially Implemented**: 7 events (through existing hooks)
- **❌ Not Implemented**: 30+ events (no detection mechanism)

## Priority Events for Future Implementation

### High Priority (User Experience Impact)
1. **SessionTimeout/Reconnect**: Critical for reliability
2. **APIError/NetworkDisconnect**: Essential for error handling
3. **ContextFull**: Important for context management
4. **UserInterrupt**: Needed for ESC key handling (Issue #13)

### Medium Priority (Enhanced Monitoring)
1. **SubagentStart/Stop**: Better subagent visibility
2. **ToolError/Timeout**: Improved error reporting
3. **ThinkingModeStart/End**: Extended thinking visibility
4. **PermissionRequest**: Security awareness

### Low Priority (Advanced Features)
1. **TranscriptWrite**: Advanced logging
2. **ConfigChange**: Dynamic configuration
3. **MicroCompact**: Detailed context management

## Technical Implementation Notes

- Hook events have 60-second execution timeout
- Exit code 0 = success, 2 = blocking error, others = non-blocking
- Common parameters: `session_id`, `transcript_path`, `cwd`
- Transcript files use JSONL format with UUIDs
- Context window limit: 200k tokens across all models
- Subagents support up to 10 concurrent operations

## References

- [Official Claude Code Hooks Documentation](https://docs.anthropic.com/en/docs/claude-code/hooks)
- [Claude Code Best Practices](https://www.anthropic.com/engineering/claude-code-best-practices)
- [Subagents Documentation](https://docs.anthropic.com/en/docs/claude-code/sub-agents)
- Community GitHub repositories and examples

---

*Last updated: September 2025 based on latest Claude Code documentation and community research*