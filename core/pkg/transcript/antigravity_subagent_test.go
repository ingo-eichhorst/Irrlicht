package transcript

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeConv writes a transcript.jsonl for conv under brain/, with the given
// body and mtime, and returns its path.
func writeConv(t *testing.T, brain, conv, body string, mtime time.Time) string {
	t.Helper()
	logs := filepath.Join(brain, conv, ".system_generated", "logs")
	if err := os.MkdirAll(logs, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(logs, "transcript.jsonl")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAntigravityParentConvID(t *testing.T) {
	brain := filepath.Join(t.TempDir(), ".gemini", "antigravity-cli", "brain")
	parent := "d363fd45-2292-4d82-b556-f36437b7f9fa"
	child := "8707b898-c6c4-446e-90e2-4f0ca42a1d42"
	now := time.Now()

	// Parent transcript references the child's conversationId (the INVOKE_SUBAGENT
	// shape). An unrelated sibling does not.
	parentBody := `{"source":"MODEL","type":"PLANNER_RESPONSE","tool_calls":[{"name":"invoke_subagent"}]}
{"source":"MODEL","type":"INVOKE_SUBAGENT","content":"Created the following subagents:\n{\n  \"conversationId\": \"` + child + `\"\n}"}
`
	writeConv(t, brain, parent, parentBody, now)
	writeConv(t, brain, "11111111-0000-0000-0000-000000000000", `{"source":"USER_EXPLICIT","type":"USER_INPUT","content":"unrelated"}`+"\n", now)
	childPath := writeConv(t, brain, child, `{"source":"USER_EXPLICIT","type":"USER_INPUT","content":"do the thing"}`+"\n", now)

	if got := AntigravityParentConvID(childPath); got != parent {
		t.Errorf("parent of child = %q, want %q", got, parent)
	}
}

func TestAntigravityParentConvID_TopLevelHasNoParent(t *testing.T) {
	brain := filepath.Join(t.TempDir(), ".gemini", "antigravity-cli", "brain")
	now := time.Now()
	a := "aaaaaaaa-0000-0000-0000-000000000000"
	b := "bbbbbbbb-0000-0000-0000-000000000000"
	writeConv(t, brain, a, `{"source":"USER_EXPLICIT","type":"USER_INPUT","content":"hi"}`+"\n", now)
	bPath := writeConv(t, brain, b, `{"source":"USER_EXPLICIT","type":"USER_INPUT","content":"hello"}`+"\n", now)
	// Neither references the other → no parent.
	if got := AntigravityParentConvID(bPath); got != "" {
		t.Errorf("top-level session got parent %q, want \"\"", got)
	}
}

func TestAntigravityParentConvID_StaleSiblingExcluded(t *testing.T) {
	// A sibling that references the child but is OLDER than the spawn window is
	// not a candidate (it can't be the parent — the parent is active at spawn).
	brain := filepath.Join(t.TempDir(), ".gemini", "antigravity-cli", "brain")
	now := time.Now()
	child := "cccccccc-0000-0000-0000-000000000000"
	staleParent := "dddddddd-0000-0000-0000-000000000000"
	writeConv(t, brain, staleParent, `{"content":"`+child+`"}`+"\n", now.Add(-10*time.Minute))
	childPath := writeConv(t, brain, child, `{"type":"USER_INPUT"}`+"\n", now)
	if got := AntigravityParentConvID(childPath); got != "" {
		t.Errorf("stale sibling matched as parent %q, want \"\" (outside spawn window)", got)
	}
}

func TestAntigravityParentConvID_NonAntigravityPath(t *testing.T) {
	if got := AntigravityParentConvID("/Users/x/.codex/sessions/2026/06/19/rollout-abc.jsonl"); got != "" {
		t.Errorf("non-antigravity path got %q, want \"\"", got)
	}
}

// TestAntigravityParentConvID_LargeParentTail guards the tail read: a parent
// transcript far larger than subagentParentTailBytes, with the child's
// conversationId in the LAST line, must still be matched (the read must fill the
// whole tail buffer, not short-read it). A convId that appears only BEFORE the
// tail window is correctly NOT matched.
func TestAntigravityParentConvID_LargeParentTail(t *testing.T) {
	brain := filepath.Join(t.TempDir(), ".gemini", "antigravity-cli", "brain")
	now := time.Now()
	parent := "eeeeeeee-0000-0000-0000-000000000000"
	child := "ffffffff-0000-0000-0000-000000000000"
	filler := strings.Repeat(`{"source":"MODEL","type":"PLANNER_RESPONSE","content":"padding line"}`+"\n", 6000) // > 256KB
	parentBody := filler + `{"source":"MODEL","type":"INVOKE_SUBAGENT","content":"conversationId: ` + child + `"}` + "\n"
	writeConv(t, brain, parent, parentBody, now)
	childPath := writeConv(t, brain, child, `{"type":"USER_INPUT"}`+"\n", now)
	if got := AntigravityParentConvID(childPath); got != parent {
		t.Errorf("child convId in the tail of a >256KB parent: got %q, want %q", got, parent)
	}

	// Same size, but the convId is only in the FIRST line (outside the tail) →
	// not matched (documents the tail-only semantics).
	brain2 := filepath.Join(t.TempDir(), ".gemini", "antigravity-cli", "brain")
	headBody := `{"source":"MODEL","type":"INVOKE_SUBAGENT","content":"conversationId: ` + child + `"}` + "\n" + filler
	writeConv(t, brain2, parent, headBody, now)
	childPath2 := writeConv(t, brain2, child, `{"type":"USER_INPUT"}`+"\n", now)
	if got := AntigravityParentConvID(childPath2); got != "" {
		t.Errorf("convId outside the tail window: got %q, want \"\"", got)
	}
}
