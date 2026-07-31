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

// TestSessionMetaPayload_RejectsParentTraversal pins the sink-local traversal
// guard: a path that resolves to a real, readable session_meta file is still
// refused when a literal ".." got into it, so neither extractor opens it.
func TestSessionMetaPayload_RejectsParentTraversal(t *testing.T) {
	real := writeSessionMeta(t, `{"type":"session_meta","payload":{"id":"`+childThreadID+`","thread_source":"subagent","parent_thread_id":"`+parentThreadID+`"}}`)
	dir := filepath.Dir(real)
	// dir/../<dir base>/<file> resolves to the same file, so only the guard —
	// not a failing open — can make the extractors return empty. Assembled by
	// hand because filepath.Join would Clean the ".." straight back out.
	sep := string(filepath.Separator)
	traversal := dir + sep + ".." + sep + filepath.Base(dir) + sep + filepath.Base(real)
	if _, err := os.Stat(traversal); err != nil {
		t.Fatalf("setup: traversal path should resolve to the real file: %v", err)
	}
	if got := sessionMetaPayload(traversal); got != nil {
		t.Errorf("sessionMetaPayload(%q) = %v; want nil (traversal rejected)", traversal, got)
	}
	if got := sessionIDFromPath(traversal); got != childThreadID {
		// The filename fallback still applies — it parses the string only.
		t.Errorf("sessionIDFromPath(%q) = %q, want the filename fallback %q", traversal, got, childThreadID)
	}
	if got := parentSessionIDFromPath(traversal); got != "" {
		t.Errorf("parentSessionIDFromPath(%q) = %q, want empty (traversal rejected)", traversal, got)
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
