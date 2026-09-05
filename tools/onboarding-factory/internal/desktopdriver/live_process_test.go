package desktopdriver

// Process ownership: the baseline census, and the bounded wait for the owned
// process to exit.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWaitProcessExitObservesTheExactCapturedPID(t *testing.T) {
	observations := 0
	runtime := &LiveRuntime{
		processes: map[string]int{"local_1": 42},
		processExists: func(pid int) (bool, error) {
			if pid != 42 {
				t.Fatalf("process check PID = %d, want 42", pid)
			}
			observations++
			return observations == 1, nil
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	owned := OwnedSession{Registry: RegistrySession{SessionID: "local_1"}, Transcript: TranscriptIdentity{SessionID: "cli-1"}}
	if err := runtime.WaitProcessExit(ctx, owned); err != nil {
		t.Fatalf("WaitProcessExit() error = %v", err)
	}
	if observations != 2 {
		t.Fatalf("process observations = %d, want 2", observations)
	}
}

func TestWaitProcessExitRefusesAnUnobservedJoinedProcess(t *testing.T) {
	runtime := &LiveRuntime{processes: map[string]int{}, processExists: liveProcessExists}
	owned := OwnedSession{Registry: RegistrySession{SessionID: "local_1"}, Transcript: TranscriptIdentity{SessionID: "cli-1"}}
	err := runtime.WaitProcessExit(context.Background(), owned)
	if err == nil || !strings.Contains(err.Error(), "was never observed") {
		t.Fatalf("WaitProcessExit() error = %v", err)
	}
}

func TestOwnedProcessCannotReuseBaselinePID(t *testing.T) {
	baseline := map[int]struct{}{42: {}}
	candidate := SessionObservation{PID: 42}
	if err := validateOwnedProcessBaseline(baseline, candidate); err == nil {
		t.Fatal("validateOwnedProcessBaseline() accepted a baseline PID")
	}
}

func TestWaitIrrlichtStateInvokesBaselinePIDGuard(t *testing.T) {
	candidate := SessionObservation{
		SessionID: "cli-1", CWD: "/repo/workspace", PID: 42, State: "ready",
		Launcher: Launcher{HostBundleID: desktopBundleID},
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if err := json.NewEncoder(writer).Encode([]SessionObservation{candidate}); err != nil {
			t.Errorf("encode session response: %v", err)
		}
	}))
	defer server.Close()
	runtime := &LiveRuntime{
		options:         LiveOptions{DaemonAddress: strings.TrimPrefix(server.URL, "http://")},
		httpClient:      server.Client(),
		processBaseline: map[int]struct{}{42: {}},
		processes:       map[string]int{},
		processEvidence: map[string]ProcessEvidence{},
		observeProcess: func(context.Context, int) (string, error) {
			return "/Applications/Claude.app/claude", nil
		},
	}
	owned := OwnedSession{
		Registry:   RegistrySession{SessionID: "local_1", CWD: "/repo/workspace"},
		Transcript: TranscriptIdentity{SessionID: "cli-1", CWD: "/repo/workspace"},
	}
	_, err := runtime.WaitIrrlichtState(context.Background(), owned, "ready")
	if err == nil || !strings.Contains(err.Error(), "reused baseline process PID 42") {
		t.Fatalf("WaitIrrlichtState() baseline PID error = %v", err)
	}
}
