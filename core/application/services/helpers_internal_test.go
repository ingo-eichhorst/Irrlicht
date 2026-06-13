package services

import "testing"

// Workflow-tool fan-out (issue #565): agents write transcripts one level
// deeper than plain Task subagents — .../<parent>/subagents/workflows/<run>/
// — alongside a journal.jsonl bookkeeping file that is not a session.

func TestDeriveParentSessionID(t *testing.T) {
	cases := []struct {
		name string
		path string
		want string
	}{
		{"plain subagent", "/p/-Users-x/parent-123/subagents/agent-abc.jsonl", "parent-123"},
		{"workflow agent", "/p/-Users-x/parent-123/subagents/workflows/wf_854deede-0ff/agent-abc.jsonl", "parent-123"},
		{"top-level session", "/p/-Users-x/parent-123.jsonl", ""},
		{"workflows dir without subagents above", "/p/-Users-x/parent-123/workflows/wf_1/agent-abc.jsonl", ""},
		{"nested below the run dir", "/p/parent/subagents/workflows/wf_1/nested/agent-a.jsonl", ""},
		{"empty path", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := deriveParentSessionID(tc.path); got != tc.want {
				t.Errorf("deriveParentSessionID(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

// Gemini CLI subagents (issue #663): the child transcript is written to a
// nested path chats/<parentId>/session-<childId>.jsonl. The parent session id
// is the directory name under chats/.
func TestDeriveParentSessionGeminiNested(t *testing.T) {
	cases := []struct {
		name string
		path string
		want string
	}{
		{"gemini nested subagent", "/h/.gemini/tmp/proj/chats/parent-123/session-child-abc.jsonl", "parent-123"},
		{"gemini top-level session", "/h/.gemini/tmp/proj/chats/session-main.jsonl", ""},
		{"non-gemini chats lookalike", "/p/-Users-x/parent-123/subagents/agent-abc.jsonl", "parent-123"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := deriveParentSession(tc.path); got != tc.want {
				t.Errorf("deriveParentSession(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

func TestIsWorkflowBookkeepingFile(t *testing.T) {
	cases := []struct {
		name string
		path string
		want bool
	}{
		{"journal in run dir", "/p/parent/subagents/workflows/wf_1/journal.jsonl", true},
		{"agent transcript in run dir", "/p/parent/subagents/workflows/wf_1/agent-a.jsonl", false},
		{"journal-named session at top level", "/p/-Users-x/journal.jsonl", false},
		{"journal in plain subagents dir", "/p/parent/subagents/journal.jsonl", false},
		{"empty path", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isWorkflowBookkeepingFile(tc.path); got != tc.want {
				t.Errorf("isWorkflowBookkeepingFile(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}
