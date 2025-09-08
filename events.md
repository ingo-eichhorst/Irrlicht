# Irrlicht App Event Analysis

## Overview

This document provides a comprehensive analysis of all events that can occur in the Irrlicht macOS menu bar application and how they are currently handled. The app follows a reactive architecture where Claude Code hook events trigger state changes that flow through the SwiftUI interface.

## Event Categories

### 1. User Interaction Events

#### 1.1 Menu Bar Icon Events
- **Event**: Menu bar icon click
- **Handler**: MenuBarIcon tap gesture in `IrrlichtApp.swift`
- **State Impact**: Shows/hides dropdown menu
- **Status**: ✅ Fully implemented
- **Location**: `Irrlicht/IrrlichtApp.swift:43-45`

- **Event**: Menu bar icon hover
- **Handler**: Not implemented
- **State Impact**: Could show quick status preview
- **Status**: ❌ Not implemented
- **Notes**: Potential enhancement for quick status indication

#### 1.2 Session List Events
- **Event**: Session row click/tap
- **Handler**: `onTapGesture` in `SessionListView.swift`
- **State Impact**: Session selection (placeholder implementation)
- **Status**: 🟡 Partially implemented
- **Location**: `SessionListView.swift:141-144`
- **Notes**: Currently only prints session ID, no actual functionality

- **Event**: Session row hover
- **Handler**: `onHover` modifier in `SessionRowView`
- **State Impact**: Shows/hides action buttons
- **Status**: ✅ Fully implemented
- **Location**: `SessionListView.swift:316-321`

- **Event**: Session drag initiation
- **Handler**: `onDrag` modifier in `SessionListView.swift`
- **State Impact**: Creates draggable item provider
- **Status**: ✅ Fully implemented
- **Location**: `SessionListView.swift:145-147`

- **Event**: Session drop/reorder
- **Handler**: `SessionDropDelegate` class
- **State Impact**: Reorders sessions in SessionManager
- **Status**: ✅ Fully implemented
- **Location**: `SessionListView.swift:389-431`

#### 1.3 Action Button Events
- **Event**: Reset session button click
- **Handler**: `SessionActionButtons` reset action
- **State Impact**: Calls `sessionManager.resetSessionState()`
- **Status**: ✅ Fully implemented
- **Location**: `SessionListView.swift:333-341`

- **Event**: Delete session button click
- **Handler**: `SessionActionButtons` delete action
- **State Impact**: Calls `sessionManager.deleteSession()`
- **Status**: ✅ Fully implemented
- **Location**: `SessionListView.swift:344-352`

#### 1.4 Application Menu Events
- **Event**: Quit button click
- **Handler**: `NSApplication.shared.terminate(nil)`
- **State Impact**: Terminates application
- **Status**: ✅ Fully implemented
- **Location**: `SessionListView.swift:24-26`

- **Event**: Quit button hover
- **Handler**: Hover state management with cursor changes
- **State Impact**: Visual feedback and cursor change
- **Status**: ✅ Fully implemented
- **Location**: `SessionListView.swift:31-43`

#### 1.5 Keyboard Events
- **Event**: ESC key press
- **Handler**: Not implemented
- **State Impact**: Should close dropdown or cancel operations
- **Status**: ❌ Not implemented
- **Notes**: Mentioned in issue #13 as stuck waiting states problem

- **Event**: Keyboard shortcuts (Cmd+Q, etc.)
- **Handler**: Standard macOS shortcuts
- **State Impact**: Standard app controls
- **Status**: ✅ Partially implemented (standard macOS handling)

### 2. System Events

#### 2.1 Application Lifecycle Events
- **Event**: App launch
- **Handler**: `IrrlichtApp` initialization
- **State Impact**: Initializes SessionManager and UI
- **Status**: ✅ Fully implemented
- **Location**: `Irrlicht/IrrlichtApp.swift`

- **Event**: App become active/inactive
- **Handler**: Not explicitly handled
- **State Impact**: Could pause/resume monitoring
- **Status**: ❌ Not implemented

- **Event**: System sleep/wake
- **Handler**: Not implemented
- **State Impact**: Could affect file monitoring
- **Status**: ❌ Not implemented

#### 2.2 Window Management Events
- **Event**: Focus loss (clicking outside dropdown)
- **Handler**: Standard SwiftUI behavior
- **State Impact**: Closes dropdown menu
- **Status**: ✅ Fully implemented (SwiftUI default)

- **Event**: Multiple displays/screen changes
- **Handler**: Standard macOS handling
- **State Impact**: Menu bar repositioning
- **Status**: ✅ System handled

### 3. File System Events

#### 3.1 Session State File Events
- **Event**: New session file created
- **Handler**: `DirectoryWatcher` in `SessionManager.swift`
- **State Impact**: Adds new session to state
- **Status**: ✅ Fully implemented
- **Location**: `SessionManager.swift:fileWatcher` setup

- **Event**: Session file modified
- **Handler**: File system monitoring in `SessionManager`
- **State Impact**: Updates existing session state
- **Status**: ✅ Fully implemented

- **Event**: Session file deleted
- **Handler**: File monitoring detects removal
- **State Impact**: Removes session from state
- **Status**: ✅ Fully implemented

#### 3.2 Directory Structure Events
- **Event**: Irrlicht directory created
- **Handler**: Directory creation in `SessionManager.init()`
- **State Impact**: Enables file monitoring
- **Status**: ✅ Fully implemented
- **Location**: `SessionManager.swift` initialization

- **Event**: Directory permissions changed
- **Handler**: Not explicitly handled
- **State Impact**: Could break file monitoring
- **Status**: ❌ Not implemented

### 4. Timer Events

#### 4.1 Periodic Refresh Events
- **Event**: 1-second UI refresh timer
- **Handler**: `TimelineView(.periodic)` in various views
- **State Impact**: Updates relative timestamps and real-time metrics
- **Status**: ✅ Fully implemented
- **Location**: `SessionListView.swift:221-227`, `257-310`

- **Event**: File monitoring refresh
- **Handler**: Internal file system monitoring
- **State Impact**: Checks for file changes
- **Status**: ✅ Fully implemented

#### 4.2 Background Tasks
- **Event**: Automatic session cleanup
- **Handler**: Not implemented
- **State Impact**: Could remove old/stale sessions
- **Status**: ❌ Not implemented

### 5. Data Events

#### 5.1 JSON Parsing Events
- **Event**: Session JSON decode success
- **Handler**: `SessionState.init(from decoder:)`
- **State Impact**: Creates/updates session object
- **Status**: ✅ Fully implemented
- **Location**: `SessionState.swift:144-201`

- **Event**: Session JSON decode failure
- **Handler**: Error handling in decoder
- **State Impact**: Logs error, may skip malformed session
- **Status**: ✅ Fully implemented with fallbacks

#### 5.2 State Validation Events
- **Event**: Invalid session state detection
- **Handler**: Validation logic in SessionState
- **State Impact**: Uses fallback values
- **Status**: ✅ Fully implemented

### 6. Claude Code Hook Events

#### 6.1 Session Lifecycle Events
- **Event**: `SessionStart`
- **Handler**: Go hook receiver → JSON file → SessionManager
- **State Impact**: Creates new session with `working` state
- **Status**: ✅ Fully implemented
- **Location**: `tools/irrlicht-hook/main.go:685-686`

- **Event**: `SessionEnd`
- **Handler**: Hook receiver processing
- **State Impact**: Sets session to `finished` state
- **Status**: ✅ Fully implemented
- **Location**: `tools/irrlicht-hook/main.go:773`

#### 6.2 User Interaction Events
- **Event**: `UserPromptSubmit`
- **Handler**: Hook receiver → state transition
- **State Impact**: Sets session to `working` state
- **Status**: ✅ Fully implemented

- **Event**: `Notification` (Claude needs user input)
- **Handler**: Hook receiver processing
- **State Impact**: Sets session to `waiting` state
- **Status**: ✅ Fully implemented

#### 6.3 Tool Execution Events
- **Event**: `PreToolUse`
- **Handler**: Hook receiver
- **State Impact**: Sets session to `working` state
- **Status**: ✅ Fully implemented

- **Event**: `PostToolUse`
- **Handler**: Hook receiver
- **State Impact**: Maintains `working` state or transitions based on context
- **Status**: ✅ Fully implemented

#### 6.4 System Events
- **Event**: `PreCompact` (context window compaction)
- **Handler**: Hook receiver
- **State Impact**: Sets session to `working` state
- **Status**: ✅ Fully implemented

- **Event**: `Stop` (session stopped/cancelled)
- **Handler**: Hook receiver
- **State Impact**: Sets session to `finished` state
- **Status**: ✅ Fully implemented

- **Event**: `SubagentStop`
- **Handler**: Hook receiver
- **State Impact**: Sets session to `finished` state
- **Status**: ✅ Fully implemented

### 7. Error Handling Events

#### 7.1 File System Errors
- **Event**: File read/write errors
- **Handler**: Error logging in SessionManager
- **State Impact**: Sets `lastError` property for UI display
- **Status**: ✅ Fully implemented
- **Location**: `SessionListView.swift:17-20` (error display)

- **Event**: Permissions errors
- **Handler**: Basic error handling
- **State Impact**: Shows error in UI
- **Status**: 🟡 Partially implemented

#### 7.2 Hook Processing Errors
- **Event**: Malformed hook event
- **Handler**: Error logging in Go hook receiver
- **State Impact**: Event may be ignored
- **Status**: ✅ Fully implemented

- **Event**: Hook binary not found
- **Handler**: Not handled in UI
- **State Impact**: Hook events don't arrive
- **Status**: ❌ Not handled (related to issue #21)

### 8. Potential Future Events (Not Yet Implemented)

#### 8.1 Enhanced User Interactions
- **Event**: Double-click session row
- **Handler**: Not implemented
- **State Impact**: Could open session details or navigate to project
- **Status**: ❌ Not implemented

- **Event**: Right-click context menu
- **Handler**: Not implemented
- **State Impact**: Could show additional actions
- **Status**: ❌ Not implemented

- **Event**: Session search/filter
- **Handler**: Not implemented
- **State Impact**: Filter visible sessions
- **Status**: ❌ Not implemented

#### 8.2 Advanced Features
- **Event**: Session export/import
- **Handler**: Not implemented
- **State Impact**: Data portability
- **Status**: ❌ Not implemented

- **Event**: Preference changes
- **Handler**: Not implemented
- **State Impact**: Customize app behavior
- **Status**: ❌ Not implemented

- **Event**: Theme/appearance changes
- **Handler**: Not implemented
- **State Impact**: UI customization
- **Status**: ❌ Not implemented

## State Flow Architecture

### Primary State Flow
```
Claude Code Event → Hook Receiver (Go) → JSON File Write → File System Monitoring → SessionManager Update → SwiftUI Re-render
```

### User Interaction Flow
```
User Action → SwiftUI Event Handler → SessionManager Method → State Update → UI Refresh
```

## Critical Event Handling Gaps

1. **ESC Key Handling**: Not implemented (issue #13)
2. **Hook Configuration Detection**: Not handled in UI (issue #21)
3. **Permission Error Recovery**: Limited handling
4. **Network/Connectivity Issues**: Not handled
5. **Concurrent Access Issues**: Potential race conditions
6. **Memory Management**: No active cleanup of old sessions

## Recommendations

### High Priority
1. Implement ESC key handling for better UX
2. Add hook configuration validation and user guidance
3. Improve error handling and recovery mechanisms
4. Add session cleanup for finished sessions

### Medium Priority
1. Add keyboard shortcuts for common actions
2. Implement session search/filtering
3. Add right-click context menus
4. Enhance drag-and-drop functionality

### Low Priority
1. Add preferences/settings
2. Implement session export/import
3. Add theme customization
4. Enhance error reporting and logging

## Event Handling Performance

- **UI Responsiveness**: 1-second refresh cycle maintains smooth UX
- **File System Monitoring**: Efficient native macOS file watching
- **State Updates**: Reactive SwiftUI updates only changed views
- **Memory Usage**: Minimal state retention, but no automatic cleanup

## Conclusion

The Irrlicht app has a well-implemented core event system with strong hook event handling and basic UI interactions. The main gaps are in advanced user interactions, error recovery, and system integration features. The reactive architecture provides a solid foundation for extending event handling capabilities.