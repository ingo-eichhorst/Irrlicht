package gastown

import "testing"

// TestBuildRigCodebase_DogWorkerReachesMainWorktree guards a regression:
// buildRigCodebase used to copy mainWorktree into the worktrees slice
// (a value type) BEFORE merging in dog workers, so a rig-assigned dog's
// worker entry updated only the disconnected local mainWorktree variable
// and never reached the Codebase the function returns.
func TestBuildRigCodebase_DogWorkerReachesMainWorktree(t *testing.T) {
	rig := rigState{Name: "rig-a", Status: "active"}
	dogs := []dogState{
		{Name: "fido", State: "working", Worktrees: map[string]string{"rig-a": "/gt/rig-a/dogs/fido"}},
	}

	cb := buildRigCodebase("/gt", rig, nil, dogs, nil, sessionIndex{})

	if len(cb.Worktrees) == 0 {
		t.Fatal("buildRigCodebase returned no worktrees")
	}
	main := cb.Worktrees[0]
	if !main.IsMain {
		t.Fatalf("Worktrees[0].IsMain = false, want the main worktree first")
	}
	found := false
	for _, w := range main.Workers {
		if w.Role == RoleDog && w.Name == "fido" {
			found = true
		}
	}
	if !found {
		t.Fatalf("main worktree Workers = %+v, want a %q worker named %q", main.Workers, RoleDog, "fido")
	}
}
