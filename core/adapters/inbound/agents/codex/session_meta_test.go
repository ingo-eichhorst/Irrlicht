package codex

import (
	"os"
	"path/filepath"
	"testing"

	"irrlicht/core/domain/agent"
)

const (
	parentThreadID = "019f6249-055d-76d3-b381-cd9d3eb99189"
	childThreadID  = "019f624a-cc95-7c50-b9fa-1db381270b73"
)

func writeSessionMeta(t *testing.T, record string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rollout-2026-07-14T20-21-37-"+childThreadID+".jsonl")
	if err := os.WriteFile(path, []byte(record+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSessionMeta_UsesThreadIdentityAndAuthoritativeParent(t *testing.T) {
	path := writeSessionMeta(t, `{"type":"session_meta","payload":{"id":"`+childThreadID+`","session_id":"shared-tree-id","thread_source":"subagent","source":{"subagent":{"thread_spawn":{"parent_thread_id":"`+parentThreadID+`"}}}}}`)
	if got := sessionIDFromPath(path); got != childThreadID {
		t.Errorf("sessionIDFromPath() = %q, want child thread ID %q", got, childThreadID)
	}
	if got := parentSessionIDFromPath(path); got != parentThreadID {
		t.Errorf("parentSessionIDFromPath() = %q, want %q", got, parentThreadID)
	}
}

func TestParentSessionIDFromPath_CompatibleFallbacks(t *testing.T) {
	tests := []struct {
		name, payload string
		want          string
	}{
		{"parent_thread_id", `{"id":"` + childThreadID + `","thread_source":"subagent","parent_thread_id":"` + parentThreadID + `"}`, parentThreadID},
		{"forked_from_id", `{"id":"` + childThreadID + `","thread_source":"subagent","forked_from_id":"` + parentThreadID + `"}`, parentThreadID},
		{"top level", `{"id":"` + childThreadID + `","thread_source":"user","forked_from_id":"` + parentThreadID + `"}`, ""},
		{"plain fork", `{"id":"` + childThreadID + `","forked_from_id":"` + parentThreadID + `"}`, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := writeSessionMeta(t, `{"type":"session_meta","payload":`+tc.payload+`}`)
			if got := parentSessionIDFromPath(path); got != tc.want {
				t.Errorf("parentSessionIDFromPath() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSessionIDFromPath_FallsBackToRolloutThreadID(t *testing.T) {
	path := writeSessionMeta(t, `not json`)
	if got := sessionIDFromPath(path); got != childThreadID {
		t.Errorf("sessionIDFromPath() = %q, want filename thread ID %q", got, childThreadID)
	}
	if got := parentSessionIDFromPath(path); got != "" {
		t.Errorf("parentSessionIDFromPath() = %q, want empty for malformed metadata", got)
	}
}

func TestAgent_DeclaresCodexMetadataExtractors(t *testing.T) {
	source, ok := Agent().Source.(agent.FilesUnderRoot)
	if !ok {
		t.Fatalf("Agent Source = %T, want agent.FilesUnderRoot", Agent().Source)
	}
	if source.SessionIDFromPath == nil || source.ParentSessionIDFromPath == nil {
		t.Fatal("Codex source must declare both metadata extractors")
	}
}
