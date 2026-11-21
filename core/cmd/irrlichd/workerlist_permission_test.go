package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"irrlicht/core/adapters/inbound/orchestrators/workerlist"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/permission"
	"irrlicht/core/internal/contracttesting"
)

// workerlistObserveDeadline bounds how long Observe waits for the pool to
// reach the monitor: snapshots travel watcher → drain goroutine → Run, so
// absence is only believable after a wait. Each arm that expects absence
// spends the whole deadline; the granted arm returns as soon as it sees it.
const workerlistObserveDeadline = 2 * time.Second

// TestWorkerlistPermission_GateContract drives the REAL effects behind
// "workerlist/state" (workerlistEffects, as main.go builds them, over a temp
// status file) through the shared consent contract (#2148). The effect it
// observes is the subject /api/v1/sessions reads: the orchestrator monitor
// holding a running worker-list state.
//
// Seen red by: building and starting the watcher when the effects are
// constructed instead of in Apply (pending_no_effect fails); and by dropping
// the monitor's registration check in OrchestratorMonitor.State (the state
// outlives the revoke, so revoked_effect_undone fails).
func TestWorkerlistPermission_GateContract(t *testing.T) {
	path := filepath.Join(t.TempDir(), workerlist.DefaultFileName)
	doc := `{"schema": 1, "teams": [{"name": "team-a", "updated": "2026-11-11T15:40:00Z",
	  "workers": [{"name": "team-a-m1-1", "busy": false}]}]}`
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}

	orchMonitor := services.NewOrchestratorMonitor(nil, services.NewPushService(), e2eLog{})
	orchCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = orchMonitor.Run(orchCtx) }()
	start, stop := workerlistEffects(orchCtx, orchMonitor, path, e2eLog{})
	perm := workerlist.PermissionDeclaration(start, stop).Permissions[0]

	contracttesting.AssertPermissionGated(t, contracttesting.PermissionGate{
		Key: perm.Key,
		// The declaration exports exactly one permission, so the key held
		// open beside it is a foreign one with no closure here to drive: the
		// key-isolation arm is INERT and repeats the revoked arm exactly
		// (contracttesting.OnlyKey's doc). Wired anyway because the contract
		// admits no opt-out.
		OtherKeys: []string{agent.HooksPermissionKey},
		SetState: contracttesting.OnlyKey(perm.Key, func(state permission.State) {
			switch state {
			case permission.StateGranted:
				if err := perm.Apply(); err != nil {
					t.Errorf("Apply: %v", err)
				}
			case permission.StateDenied:
				if err := perm.Remove(); err != nil {
					t.Errorf("Remove: %v", err)
				}
			}
		}),
		Exercise: func() {}, // the effect IS the Apply/Remove call above
		Observe: func() bool {
			return pollUntil(t, workerlistObserveDeadline, 5*time.Millisecond, func() bool {
				st := orchMonitor.State(workerlist.Name)
				return st != nil && st.Running
			})
		},
	})
}
