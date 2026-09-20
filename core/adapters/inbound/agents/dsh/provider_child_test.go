package dsh

import (
	"context"
	"errors"
	"testing"
)

// Mutation fixture: removing codexWrapperArgv from the Codex lineage check made
// "wrapper changed" and "shell child" fail under go test ./core/adapters/inbound/agents/dsh
// -run TestOwnedProviderChildRequiresExactLineageAndVisibleDSH -count=1.
func TestOwnedProviderChildRequiresExactLineageAndVisibleDSH(t *testing.T) {
	const dshPID, wrapperPID, childPID = 10, 20, 30
	baseArgv := map[int][]string{
		dshPID:     {"node", "/opt/dsh/bin/dsh", "run"},
		wrapperPID: {"node", "/opt/dsh/node_modules/@openai/codex/bin/codex.js", "app-server", "--stdio"},
		childPID:   {"/opt/codex", "app-server", "--stdio"},
	}
	baseParents := map[int]int{childPID: wrapperPID, wrapperPID: dshPID}
	tests := []struct {
		name     string
		provider string
		argv     map[int][]string
		parents  map[int]int
		visible  bool
		want     bool
	}{
		{"codex owned", "codex", nil, nil, true, true},
		{"parent not visible", "codex", nil, nil, false, false},
		{"child argv unreadable", "codex", map[int][]string{childPID: nil}, nil, true, false},
		{"wrapper changed", "codex", map[int][]string{wrapperPID: {"node", "/tmp/other.js", "app-server", "--stdio"}}, nil, true, false},
		{"shell child", "codex", map[int][]string{wrapperPID: {"sh", "-c", "codex app-server --stdio"}}, nil, true, false},
		{"unrelated launcher", "codex", map[int][]string{dshPID: {"node", "/tmp/other"}}, nil, true, false},
		{"broken parent read", "codex", nil, map[int]int{wrapperPID: 0}, true, false},
		{"cycle", "codex", nil, map[int]int{wrapperPID: wrapperPID}, true, false},
		{"claude owned", "claude-code", map[int][]string{childPID: {"/opt/claude", "--input-format", "stream-json", "--output-format", "stream-json", "--no-session-persistence"}}, map[int]int{childPID: dshPID}, true, true},
		{"claude persistent", "claude-code", map[int][]string{childPID: {"/opt/claude", "--input-format", "stream-json", "--output-format", "stream-json"}}, map[int]int{childPID: dshPID}, true, false},
		{"claude through shell", "claude-code", map[int][]string{childPID: {"/opt/claude", "--input-format", "stream-json", "--output-format", "stream-json", "--no-session-persistence"}}, nil, true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			argv := make(map[int][]string)
			for pid, value := range baseArgv {
				argv[pid] = value
			}
			for pid, value := range tc.argv {
				argv[pid] = value
			}
			parents := make(map[int]int)
			for pid, value := range baseParents {
				parents[pid] = value
			}
			for pid, value := range tc.parents {
				parents[pid] = value
			}
			got := ownsProviderChildVia(context.Background(), tc.provider, childPID,
				func(pid int) []string { return argv[pid] },
				func(_ context.Context, pid int) (int, error) {
					parent := parents[pid]
					if parent == 0 {
						return 0, errors.New("unreadable")
					}
					return parent, nil
				},
				func(pid int) bool { return pid == dshPID && tc.visible })
			if got != tc.want {
				t.Fatalf("owned = %t, want %t", got, tc.want)
			}
		})
	}
}
