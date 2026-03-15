package cursor_test

import (
	"testing"

	cursorAdapter "irrlicht/core/adapters/inbound/cursor"
	cursorev "irrlicht/core/domain/cursor"
)

func TestNormalizeEvent_EventNameMapping(t *testing.T) {
	cases := []struct {
		cursorEvent  string
		wantIrrlicht string
	}{
		{"sessionStart", "SessionStart"},
		{"sessionEnd", "SessionEnd"},
		{"stop", "Stop"},
		{"subagentStart", "SessionStart"},
		{"subagentStop", "SubagentStop"},
		{"preToolUse", "PreToolUse"},
		{"postToolUse", "PostToolUse"},
		{"postToolUseFailure", "PostToolUse"},
		{"beforeSubmitPrompt", "UserPromptSubmit"},
		{"beforeShellExecution", "PreToolUse"},
		{"afterShellExecution", "PostToolUse"},
		{"preCompact", "PreCompact"},
		{"afterAgentThought", "PreToolUse"},
	}

	for _, tc := range cases {
		t.Run(tc.cursorEvent, func(t *testing.T) {
			evt := &cursorev.CursorEvent{
				HookEventName:  tc.cursorEvent,
				ConversationID: "conv_test123",
			}
			normalized := cursorAdapter.NormalizeEvent(evt)
			if normalized.HookEventName != tc.wantIrrlicht {
				t.Errorf("NormalizeEvent(%q).HookEventName = %q, want %q",
					tc.cursorEvent, normalized.HookEventName, tc.wantIrrlicht)
			}
		})
	}
}

func TestNormalizeEvent_SessionIDPrefixing(t *testing.T) {
	evt := &cursorev.CursorEvent{
		HookEventName:  "preToolUse",
		ConversationID: "conv_abc123",
	}
	normalized := cursorAdapter.NormalizeEvent(evt)
	want := "cursor_conv_abc123"
	if normalized.SessionID != want {
		t.Errorf("SessionID = %q, want %q", normalized.SessionID, want)
	}
}

func TestNormalizeEvent_WorkspaceRootsToCWD(t *testing.T) {
	evt := &cursorev.CursorEvent{
		HookEventName:  "preToolUse",
		ConversationID: "conv_xyz",
		WorkspaceRoots: []string{"/home/user/project", "/home/user/other"},
	}
	normalized := cursorAdapter.NormalizeEvent(evt)
	want := "/home/user/project"
	if normalized.CWD != want {
		t.Errorf("CWD = %q, want %q (should take first root)", normalized.CWD, want)
	}
}

func TestNormalizeEvent_EmptyWorkspaceRoots(t *testing.T) {
	evt := &cursorev.CursorEvent{
		HookEventName:  "preToolUse",
		ConversationID: "conv_xyz",
	}
	normalized := cursorAdapter.NormalizeEvent(evt)
	if normalized.CWD != "" {
		t.Errorf("CWD = %q, want empty string for no workspace roots", normalized.CWD)
	}
}

func TestNormalizeEvent_SessionStartMatcher(t *testing.T) {
	cases := []struct {
		source      string
		wantMatcher string
	}{
		{"new", "startup"},
		{"resume", "resume"},
		{"", "startup"},
		{"unknown", "startup"},
	}

	for _, tc := range cases {
		t.Run("source="+tc.source, func(t *testing.T) {
			evt := &cursorev.CursorEvent{
				HookEventName:  "sessionStart",
				ConversationID: "conv_sess",
				Source:         tc.source,
			}
			normalized := cursorAdapter.NormalizeEvent(evt)
			if normalized.Matcher != tc.wantMatcher {
				t.Errorf("Matcher = %q, want %q", normalized.Matcher, tc.wantMatcher)
			}
		})
	}
}

func TestNormalizeEvent_SessionEndReason(t *testing.T) {
	evt := &cursorev.CursorEvent{
		HookEventName:  "sessionEnd",
		ConversationID: "conv_end",
		Reason:         "user_exit",
	}
	normalized := cursorAdapter.NormalizeEvent(evt)
	if normalized.Reason != "user_exit" {
		t.Errorf("Reason = %q, want %q", normalized.Reason, "user_exit")
	}
}

func TestNormalizeEvent_PreCompactMatcher(t *testing.T) {
	for _, compactType := range []string{"auto", "manual"} {
		evt := &cursorev.CursorEvent{
			HookEventName:  "preCompact",
			ConversationID: "conv_compact",
			CompactType:    compactType,
		}
		normalized := cursorAdapter.NormalizeEvent(evt)
		if normalized.Matcher != compactType {
			t.Errorf("Matcher = %q, want %q for preCompact compact_type=%q",
				normalized.Matcher, compactType, compactType)
		}
	}
}

func TestNormalizeEvent_ShellExecutionToolName(t *testing.T) {
	for _, eventName := range []string{"beforeShellExecution", "afterShellExecution"} {
		evt := &cursorev.CursorEvent{
			HookEventName:  eventName,
			ConversationID: "conv_shell",
			Command:        "ls -la",
		}
		normalized := cursorAdapter.NormalizeEvent(evt)
		if normalized.ToolName != "shell" {
			t.Errorf("%s: ToolName = %q, want %q", eventName, normalized.ToolName, "shell")
		}
	}
}

func TestNormalizeEvent_SubagentParentLinkage(t *testing.T) {
	evt := &cursorev.CursorEvent{
		HookEventName:        "subagentStop",
		ConversationID:       "conv_child",
		ParentConversationID: "conv_parent",
	}
	normalized := cursorAdapter.NormalizeEvent(evt)
	want := "cursor_conv_parent"
	if normalized.ParentSessionID != want {
		t.Errorf("ParentSessionID = %q, want %q", normalized.ParentSessionID, want)
	}
}

func TestNormalizeEvent_UnknownEventFallback(t *testing.T) {
	evt := &cursorev.CursorEvent{
		HookEventName:  "someUnknownEvent",
		ConversationID: "conv_unknown",
	}
	normalized := cursorAdapter.NormalizeEvent(evt)
	// Unknown events should produce "PreToolUse" to keep session in working state.
	if normalized.HookEventName != "PreToolUse" {
		t.Errorf("Unknown event HookEventName = %q, want PreToolUse", normalized.HookEventName)
	}
}

func TestIsApprovalProne(t *testing.T) {
	prone := []string{"shell", "bash", "exec_cmd", "RunBash", "WriteFile", "EditFile",
		"CreateFile", "delete_path", "BASH_tool", "write_to_file"}
	notProne := []string{"read", "list", "search", "grep", "cat", "view", "ReadFile"}

	for _, name := range prone {
		if !cursorAdapter.IsApprovalProne(name) {
			t.Errorf("IsApprovalProne(%q) = false, want true", name)
		}
	}
	for _, name := range notProne {
		if cursorAdapter.IsApprovalProne(name) {
			t.Errorf("IsApprovalProne(%q) = true, want false", name)
		}
	}
}
