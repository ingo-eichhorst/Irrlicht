package codex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"irrlicht/core/internal/contracttesting"
)

// codexSessionsRoot relocates the agent home to a temp dir and returns the
// declared transcript root inside it — CODEX_HOME moves the home, and rollouts
// live in its sessions/ subdirectory. Shared by both receiving-side contracts
// so the layout is written down once.
func codexSessionsRoot(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv(codexHomeEnvVar, home)
	root := filepath.Join(home, "sessions")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("create sessions dir: %v", err)
	}
	return root
}

// writeContractTranscript writes a rollout the receiver will resolve a session
// id from. Codex reads that id out of the session_meta header, so the decoy
// outside the tree carries one too — the only thing keeping it from dispatching
// is confinement, not an unreadable file.
func writeContractTranscript(t *testing.T, dir string) string {
	t.Helper()
	return writeRolloutInto(t, dir, "sess-contract")
}

// contractPayload renders this adapter's hook body for one event.
func contractPayload(transcriptPath, event string) string {
	body, err := json.Marshal(codexHookPayload{
		TranscriptPath: transcriptPath,
		HookEventName:  event,
		ToolName:       "shell",
	})
	if err != nil {
		panic(err)
	}
	return string(body)
}

// TestHookPathConfined wires this adapter's hook receiver into the shared issue
// #1361 contract: a caller-supplied transcript_path is confined to the
// adapter's declared transcript roots, with symlinks resolved first, and
// anything outside is refused loudly and counted.
func TestHookPathConfined(t *testing.T) {
	contracttesting.AssertHookPathConfined(t, contracttesting.HookReceiver{
		Root: codexSessionsRoot,
		New: func(t *testing.T) contracttesting.HookReceiverUnderTest {
			t.Helper()
			target := &mockTarget{}
			return contracttesting.HookReceiverUnderTest{
				Handler:  NewHookHandler(target, nil, mockLogger{}),
				Observed: func() bool { return target.totalCalls() > 0 },
			}
		},
		WriteTranscript: writeContractTranscript,
		TranscriptExt:   transcriptExt,
		PayloadFor: func(transcriptPath string) string {
			return contractPayload(transcriptPath, HookPermissionRequest)
		},
		EndpointPath: HookEndpointPath,
	})
}
